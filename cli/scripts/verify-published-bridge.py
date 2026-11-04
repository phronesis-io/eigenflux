#!/usr/bin/env python3
"""Opt-in native acceptance using published binaries and an isolated local API.

No owner credentials, production API writes, OS tasks or notifications are used.
The first automatic invocation must reach v2 through the published bridge.
"""
import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import threading
import urllib.request

CDN = 'https://cdn.eigenflux.ai'


def fetch(path):
    with urllib.request.urlopen(CDN + path, timeout=60) as response:
        return response.read()


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--from-version', default='1.0.0')
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    system = {'Darwin': 'darwin', 'Linux': 'linux'}.get(platform.system())
    arch = {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'amd64'}.get(platform.machine())
    if not system or not arch:
        parser.error('run natively on macOS or Linux; Windows requires its native harness')
    artifact = f'eigenflux-{system}-{arch}'
    legacy = json.loads(fetch('/cli/latest/release.json'))['version']
    target = json.loads(fetch('/cli/v2/latest/release.json'))['version']
    initial_manifest = json.loads(fetch(f'/cli/{args.from_version}/release.json'))
    binary = fetch(f'/cli/{args.from_version}/{artifact}')
    expected = initial_manifest['artifacts'][artifact]
    require(len(binary) == expected['size'] and hashlib.sha256(binary).hexdigest() == expected['sha256'],
            'initial executable checksum or size mismatch')
    requests, events = [], []

    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def respond(self):
            requests.append({'method': self.command, 'path': self.path})
            payload = self.rfile.read(int(self.headers.get('Content-Length', '0')))
            if self.path == '/api/v2/maintenance/events:batch' and payload:
                events.extend(json.loads(payload).get('events', []))
            data = {'auto_cli_update': True} if self.path.endswith('/agents/me/settings') else {}
            body = json.dumps({'code': 0, 'data': data}).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(body)
        do_GET = do_POST = do_PUT = respond

    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    report = {'platform': artifact, 'legacy': legacy, 'target': target,
              'initial': args.from_version, 'checks': [], 'events': events, 'passed': False}
    try:
        with tempfile.TemporaryDirectory(prefix='eigenflux-bridge-') as temporary:
            root = Path(temporary).resolve()
            home = root / '.eigenflux'
            account = home / 'servers' / 'audit'
            account.mkdir(parents=True, mode=0o700)
            endpoint = f'http://127.0.0.1:{server.server_port}'
            (home / 'config.json').write_text(json.dumps({'default_server': 'audit', 'servers': [
                {'name': 'audit', 'endpoint': endpoint, 'commission_endpoint': endpoint,
                 'stream_endpoint': endpoint}], 'kv': {
                    'auto_cli_update': 'true', 'auto_skill_sync': 'false', 'auto_plugin_update': 'false'}}))
            credentials = account / 'agent-v2-credentials.json'
            credentials.write_text(json.dumps({'agent_id': 'audit-agent', 'principal_id': 'audit-principal',
                'access_token': 'local-fixture', 'refresh_token': 'local-fixture',
                'expires_at': 4102444800000}))
            credentials.chmod(0o600)
            original_credentials = credentials.read_bytes()
            executable = root / 'eigenflux'
            executable.write_bytes(binary)
            executable.chmod(0o755)
            env = {k: v for k, v in os.environ.items() if not k.startswith('EIGENFLUX_')}
            env.update(EIGENFLUX_HOME=str(home), EIGENFLUX_MODE='skill', EIGENFLUX_HOST='codex',
                       EIGENFLUX_CDN_URL=CDN, EIGENFLUX_SKIP_AGENT_SETUP='1',
                       EIGENFLUX_SKILLS_DIR=str(root / 'absent-skills'))

            def run_check(label):
                result = subprocess.run([str(executable), '--homedir', str(home), '--server', 'audit',
                    'heartbeat', 'plan', '--format', 'json'], env=env, capture_output=True, text=True, timeout=180)
                version = subprocess.check_output([str(executable), '--homedir', str(home),
                    'version', '--short'], env=env, text=True, timeout=30).strip()
                pointer = executable.with_name('eigenflux.current.json')
                entry = {'label': label, 'running_version': version, 'plan_exit_code': result.returncode,
                    'stderr': result.stderr[-6000:], 'stdout': result.stdout[-2000:],
                    'active_pointer': json.loads(pointer.read_text()) if pointer.exists() else None}
                report['checks'].append(entry)
                print(label, version, 'plan_exit', result.returncode, flush=True)
                require(credentials.read_bytes() == original_credentials, 'credentials changed')
                return version

            first = run_check('first automatic cycle')
            require(first == target, f'first cycle ran {first}, expected latest {target}')
            states = list(root.glob('eigenflux*.update.json'))
            require(bool(states), 'no updater eligibility state')
            original_states = {state: state.read_bytes() for state in states}
            immediate = run_check('immediate second automatic cycle')
            require(immediate == target, f'repeat cycle ran {immediate}, expected {target}')
            require(all(state.read_bytes() == data for state, data in original_states.items()),
                    'repeat cycle bypassed normal update throttle')
            report['immediate_reaches_target'] = True
            report['repeat_preserved_update_state'] = True
            for before, after in ((args.from_version, legacy), (legacy, target)):
                if before == after:
                    continue
                require(any(e.get('component') == 'cli' and e.get('trigger') == 'auto'
                    and e.get('result') == 'executed' and e.get('from_version') == before
                    and e.get('running_version') == after for e in events), 'missing automatic execution receipt')
            report['credential_preserved'] = True
            report['api_requests'] = requests
            report['passed'] = True
            report['scope'] = 'CLI automatic adoption only; missing Skills intentionally prevents business-plan completion'
    finally:
        server.shutdown()
        server.server_close()
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')


if __name__ == '__main__':
    main()
