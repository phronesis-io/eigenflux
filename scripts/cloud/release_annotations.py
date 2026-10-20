#!/usr/bin/env python3
"""Durable release events recorded only after a successful production rollout."""
import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
from zoneinfo import ZoneInfo
import fcntl
import hashlib
import html
import json
import os
from pathlib import Path
import re
import tempfile
import urllib.error
import urllib.parse
import urllib.request

DASHBOARDS = {'recommendation-effect', 'card-refresh', 'auto-publish', 'user-growth'}
TAG = 'eigenflux-release'
PREFIX = 'eigenflux-release-id:'
DEFAULT_STATE = Path('/var/lib/eigenflux-deployer/annotations')


def load_manifest(path):
    # Old commits predate release declarations; a rollback to them removes events.
    events = json.loads(path.read_text()) if path.exists() else []
    if not isinstance(events, list) or len(events) > 500:
        raise ValueError('release declarations must be a list of at most 500 events')
    ids = set()
    required = {'id', 'kind', 'deploy_scope', 'dashboards', 'title', 'description', 'pr'}
    for e in events:
        if not isinstance(e, dict) or set(e) != required:
            raise ValueError('invalid release declaration fields')
        if not isinstance(e['id'], str) or not re.fullmatch(r'[a-z0-9-]{1,80}', e['id']) or e['id'] in ids:
            raise ValueError('invalid or duplicate release declaration id')
        ids.add(e['id'])
        if e['kind'] not in ('behavior', 'instrumentation') or e['deploy_scope'] not in ('api', 'full'):
            raise ValueError('invalid release kind or deployment scope')
        if (not isinstance(e['dashboards'], list) or not e['dashboards']
                or any(not isinstance(v, str) or v not in DASHBOARDS for v in e['dashboards'])
                or len(set(e['dashboards'])) != len(e['dashboards'])):
            raise ValueError('unknown or duplicate dashboard scope')
        for key, limit in [('title', 160), ('description', 2000)]:
            if not isinstance(e[key], str) or not e[key].strip() or len(e[key]) > limit:
                raise ValueError('invalid release title or description')
        if not isinstance(e['pr'], str) or not re.fullmatch(r'https://github.com/phronesis-io/eigenflux/pull/[1-9][0-9]*', e['pr']):
            raise ValueError('release declaration requires its PR URL')
    return events


@contextmanager
def locked(directory, name, blocking=True):
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (directory / name).open('a') as handle:
        fcntl.flock(handle, fcntl.LOCK_EX | (0 if blocking else fcntl.LOCK_NB))
        yield


def load_state(directory):
    path = directory / 'state.json'
    return json.loads(path.read_text()) if path.exists() else {
        'version': 1, 'sequence': 0, 'active': {}, 'catalog': {}, 'pending': {}, 'attempted': []}


def save_state(directory, state):
    fd, name = tempfile.mkstemp(prefix='.state-', dir=directory)
    try:
        with os.fdopen(fd, 'w') as handle:
            json.dump(state, handle, ensure_ascii=False)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(name, directory / 'state.json')
        fd = os.open(directory, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def validate_catalog(state, events):
    for e in events:
        old = state['catalog'].get(e['id'])
        if old and old != e:
            raise ValueError('published release declarations are immutable; use a new id')


def make_payload(e, event_id, action, commit, moment):
    kind = '埋点' if e['kind'] == 'instrumentation' else '功能'
    title = kind + '｜' + ('' if action == 'activate' else '停止生效：') + e['title']
    note = e['description'] if action == 'activate' else '当前部署已移除此项声明。'
    stamp = int(moment.timestamp() * 1000)
    display_time = moment.astimezone(ZoneInfo('Asia/Shanghai')).strftime('%Y-%m-%d %H:%M')
    return {'time': stamp, 'timeEnd': stamp,
            'text': f'{html.escape(title)}<br><small style="font-weight:400">{display_time}（UTC+8） · '
                    f'<a href="{e["pr"]}">PR #{e["pr"].rsplit("/", 1)[1]}</a><br>{html.escape(note)}</small>',
            'tags': [TAG, PREFIX + event_id, 'kind:' + e['kind'], 'source:production-deployer',
                     'transition:' + action, 'deployed-commit:' + commit,
                     *['dashboard:' + d for d in e['dashboards']]]}


def record(directory, events, scope, commit, moment=None):
    if scope not in ('api', 'full') or not re.fullmatch(r'[a-f0-9]{40}', commit):
        raise ValueError('valid deployment scope and full commit required')
    moment = moment or datetime.now(timezone.utc)
    if moment.utcoffset() is None:
        raise ValueError('timezone required')
    with locked(directory, 'state.lock'):
        state = load_state(directory)
        validate_catalog(state, events)
        scopes = {'api', 'full'} if scope == 'full' else {'api'}
        before = state['active']
        after = {k: e for k, e in before.items() if e['deploy_scope'] not in scopes}
        after.update({e['id']: e for e in events if e['deploy_scope'] in scopes})
        changes = [('deactivate', before[k]) for k in sorted(before.keys() - after.keys())]
        changes += [('activate', after[k]) for k in sorted(after.keys() - before.keys())]
        for action, e in changes:
            state['sequence'] += 1
            identity = f"{state['sequence']}:{commit}:{e['id']}:{action}"
            event_id = 'deploy-' + hashlib.sha256(identity.encode()).hexdigest()[:40]
            state['pending'][event_id] = make_payload(e, event_id, action, commit, moment)
        state['catalog'].update({e['id']: e for e in events})
        state['active'] = after
        # State transition and complete outbox payload share one fsynced replacement.
        save_state(directory, state)
        return len(changes)


class Grafana:
    def __init__(self, url, token):
        if urllib.parse.urlsplit(url).scheme != 'https':
            raise ValueError('Grafana writer requires HTTPS')
        self.url, self.token = url.rstrip('/'), token

    def request(self, method, path, body=None):
        req = urllib.request.Request(self.url + path, method=method,
            headers={'Authorization': 'Bearer ' + self.token, 'Content-Type': 'application/json'},
            data=None if body is None else json.dumps(body).encode())
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                return json.load(response)
        except urllib.error.HTTPError as exc:
            raise RuntimeError(f'Grafana {method}: HTTP {exc.code}') from None
        except (urllib.error.URLError, TimeoutError):
            raise RuntimeError('Grafana request failed; retained for reconciliation') from None


def find(client, event_id):
    rows = client.request('GET', '/api/annotations?' + urllib.parse.urlencode({
        'tags': PREFIX + event_id, 'type': 'annotation', 'limit': 2}))
    if not isinstance(rows, list) or len(rows) > 1:
        raise RuntimeError('ambiguous release identity; no write attempted')
    if not rows:
        return None
    row = rows[0]
    if (TAG not in row.get('tags', []) or row.get('dashboardUID')
            or row.get('dashboardId') or row.get('panelId')):
        raise RuntimeError('release identity belongs to a foreign annotation')
    return row


def matches(row, wanted):
    return row is not None and all(row.get(k) == wanted[k] for k in ('time', 'timeEnd', 'text')) \
        and set(row.get('tags', [])) == set(wanted['tags'])


def flush(directory, client, limit=10):
    # Only the writer lock spans network I/O. Recording a healthy deployment
    # never waits on Grafana; a separate brief lock protects ledger changes.
    with locked(directory, 'writer.lock', blocking=False):
        with locked(directory, 'state.lock'):
            pending = list(load_state(directory)['pending'].items())[:limit]
        for event_id, wanted in pending:
            row = find(client, event_id)
            if row is None:
                with locked(directory, 'state.lock'):
                    state = load_state(directory)
                    attempted = state.setdefault('attempted', [])
                    if event_id in attempted:
                        raise RuntimeError('previous POST outcome unknown; reconcile or confirm absence before retry')
                    attempted.append(event_id)
                    save_state(directory, state)
                client.request('POST', '/api/annotations', wanted)
                row = find(client, event_id)
            if not matches(row, wanted):
                raise RuntimeError('release annotation differs from durable event; retained for review')
            with locked(directory, 'state.lock'):
                state = load_state(directory)
                if state['pending'].get(event_id) == wanted:
                    del state['pending'][event_id]
                    if event_id in state.get('attempted', []):
                        state['attempted'].remove(event_id)
                    save_state(directory, state)
        return len(pending)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--state-dir', type=Path, default=DEFAULT_STATE)
    commands = parser.add_subparsers(dest='command', required=True)
    for command in ('validate', 'record'):
        sub = commands.add_parser(command)
        sub.add_argument('--manifest', type=Path, required=True)
        if command == 'record':
            sub.add_argument('--scope', choices=('api', 'full'), required=True)
            sub.add_argument('--commit', required=True)
    commands.add_parser('flush')
    args = parser.parse_args()
    if args.command == 'flush':
        client = Grafana(os.environ['GRAFANA_RELEASE_URL'], os.environ['GRAFANA_RELEASE_TOKEN'])
        print(f'published/reconciled {flush(args.state_dir, client)} release events')
    else:
        events = load_manifest(args.manifest)
        if args.command == 'record':
            print(f'queued {record(args.state_dir, events, args.scope, args.commit)} release events')
        else:
            with locked(args.state_dir, 'state.lock'):
                validate_catalog(load_state(args.state_dir), events)
            print('release declarations valid')


if __name__ == '__main__':
    main()
