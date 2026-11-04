"""Exercise the PowerShell referral flow with a native CLI and loopback API."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import unittest

ROOT = Path(__file__).resolve().parents[2]
PWSH = os.environ.get('EIGENFLUX_TEST_PWSH') or shutil.which('pwsh')
REF = 'EF-Abcd1234'
RECEIPT = {'code': 0, 'data': {'converted': True, 'attribution': {'ref': REF}}}


@unittest.skipUnless(os.name == 'nt' or PWSH, 'requires PowerShell')
class WindowsAttributionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.shells = ('powershell', 'pwsh') if os.name == 'nt' else (PWSH,)
        for shell in cls.shells:
            if not shutil.which(shell):
                raise AssertionError(f'{shell} is required')
        cls.cli = Path(os.environ.get('EIGENFLUX_TEST_CLI', ROOT / 'build/eigenflux.exe'))
        if not cls.cli.is_file():
            raise AssertionError('build the native CLI and set EIGENFLUX_TEST_CLI')

    def run_flow(self, shell, responses=None, ref=REF, failure='', identity=False,
                 previous_ref=None, mismatched_endpoint=False):
        requests = []
        responses = responses or [(200, RECEIPT)]

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                requests.append((self.path, json.loads(self.rfile.read(int(self.headers['Content-Length'])))))
                status, payload = responses[min(len(requests) - 1, len(responses) - 1)]
                if status == 0:
                    self.close_connection = True
                    return
                body = (payload if isinstance(payload, str) else json.dumps(payload)).encode()
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory(prefix='eigenflux attribution ') as temp:
                directory = Path(temp)
                home = directory / '.eigenflux'
                home.mkdir()
                endpoint = f'http://127.0.0.1:{server.server_port}'
                selected = 'http://127.0.0.1:1' if mismatched_endpoint else endpoint
                (home / 'config.json').write_text(json.dumps({
                    'default_server': 'selected',
                    'servers': [{'name': 'selected', 'endpoint': selected},
                                {'name': 'other', 'endpoint': 'http://127.0.0.1:2'}],
                }))
                server_home = home / 'servers/selected'
                server_home.mkdir(parents=True)
                if identity:
                    (server_home / 'agent-v2-credentials.json').write_text(json.dumps({
                        'agent_id': '42', 'access_token': 'access', 'refresh_token': 'refresh'}))
                if previous_ref:
                    (server_home / 'install-ref.json').write_text(json.dumps({'ref': previous_ref, 'endpoint': endpoint}))
                harness = directory / 'attribution.ps1'
                harness.write_text(r'''$ErrorActionPreference = 'Stop'
$tokens = $null
$errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($env:INSTALLER_SOURCE, [ref]$tokens, [ref]$errors)
if ($errors.Count -ne 0) { throw ($errors | Out-String) }
foreach ($name in @('Info', 'Ok', 'Get-InvokingHost', 'Resolve-EigenfluxHome', 'Save-InstallReferral', 'Report-InstallAttribution')) {
    $function = $ast.Find({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name }, $true)
    if (-not $function) { throw "Missing installer function: $name" }
    Invoke-Expression $function.Extent.Text
}
function Start-Sleep { param($Seconds) }
function Install-Cli { $script:installedCliPath = $env:TEST_CLI }
function Migrate-Config { }
function Install-Skills { if ($env:TEST_FAILURE -eq 'skills') { throw 'fixture Skills failure' } }
function Setup-Agents { }
$ExplicitEigenfluxHome = $env:TEST_HOME
$source = (Get-Content -Raw -Encoding UTF8 $env:INSTALLER_SOURCE).Replace("`r`n", "`n")
$main = $source.Substring($source.IndexOf('# ── Main'))
$steps = $main.Substring($main.IndexOf("`nInstall-Cli`n") + 1)
$steps = $steps.Substring(0, $steps.IndexOf("`nSetup-Agents`n") + "`nSetup-Agents`n".Length)
$flow = [scriptblock]::Create($ast.ParamBlock.Extent.Text + "`n" + $steps)
& $flow -Ref $env:TEST_REF -Endpoint $env:TEST_ENDPOINT
''', encoding='utf-8-sig')
                env = dict(os.environ, HOME=temp, USERPROFILE=temp, TEMP=temp, TMP=temp,
                           EIGENFLUX_HOME=str(home), EIGENFLUX_SKILLS_DIR=str(directory / 'skills'),
                           EIGENFLUX_CDN_URL='http://127.0.0.1:1', INVOKING_HOST='codex',
                           INSTALLER_SOURCE=str(ROOT / 'static/install.ps1'),
                           TEST_CLI=str(self.cli), TEST_HOME=str(home), TEST_REF=ref,
                           TEST_ENDPOINT=endpoint, TEST_FAILURE=failure)
                env = {key: value for key, value in env.items() if key.upper() != 'PSMODULEPATH'}
                result = subprocess.run([shell, '-NoProfile', '-NonInteractive', '-File', str(harness)],
                                        env=env, capture_output=True, text=True, timeout=20)
                saved = server_home / 'install-ref.json'
                record = json.loads(saved.read_text()) if saved.exists() else None
                self.assertFalse((home / 'servers/other/install-ref.json').exists())
                for path, payload in requests:
                    self.assertEqual(path, '/api/v1/install/report')
                    self.assertEqual(payload['ref'], ref)
                    self.assertEqual(payload['metadata']['via'], 'install.ps1')
                return result, record, requests
        finally:
            server.shutdown()
            thread.join()
            server.server_close()

    def test_saves_selected_server_ref_and_reports_before_skills_failure(self):
        for shell in self.shells:
            with self.subTest(shell=shell):
                result, record, requests = self.run_flow(shell, failure='skills')
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('fixture Skills failure', result.stderr)
                self.assertEqual(record['ref'], REF)
                self.assertEqual(len(requests), 1)

    def test_first_ref_and_existing_identity_are_preserved(self):
        for shell in self.shells:
            for identity, previous in ((True, None), (False, 'EF-First123')):
                with self.subTest(shell=shell, identity=identity, previous=previous):
                    result, record, requests = self.run_flow(shell, identity=identity, previous_ref=previous)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(record['ref'] if record else None, previous)
                    self.assertEqual(len(requests), 1)

    def test_absent_invalid_and_mismatched_ref_do_not_report(self):
        for shell in self.shells:
            for ref, mismatch in (('', False), ('EFI-Abcd12', False), ('ef-Abcd1234', False), (REF, True)):
                with self.subTest(shell=shell, ref=ref, mismatch=mismatch):
                    result, record, requests = self.run_flow(shell, ref=ref, mismatched_endpoint=mismatch)
                    self.assertEqual(result.returncode == 0, ref == '', result.stdout + result.stderr)
                    self.assertIsNone(record)
                    self.assertEqual(requests, [])

    def test_transient_failures_retry_the_same_ref(self):
        for shell in self.shells:
            for status in (0, 408, 429, 503):
                with self.subTest(shell=shell, status=status):
                    result, record, requests = self.run_flow(shell, responses=[(status, {}), (200, RECEIPT)])
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(record['ref'], REF)
                    self.assertEqual(len(requests), 2)
                    self.assertIn('Install attributed', result.stdout)

    def test_permanent_and_exhausted_failures_continue_without_false_success(self):
        for shell in self.shells:
            for status, body, attempts in ((404, {}, 1), (503, {}, 3), (200, {'code': 0}, 3),
                                           (200, {'code': '0', 'data': RECEIPT['data']}, 3),
                                           (200, '<html>invalid response</html>', 3)):
                with self.subTest(shell=shell, status=status, body=body):
                    result, record, requests = self.run_flow(shell, responses=[(status, body)])
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(record['ref'], REF)
                    self.assertEqual(len(requests), attempts)
                    self.assertNotIn('Install attributed', result.stdout)
                    self.assertIn('reporting is incomplete', result.stdout)


if __name__ == '__main__':
    unittest.main()
