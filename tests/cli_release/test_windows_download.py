"""Run the production PowerShell download helper against a local HTTP server."""
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import unittest

ROOT = Path(__file__).resolve().parents[2]


@unittest.skipUnless(os.name == 'nt', 'requires Windows PowerShell 5.1 and PowerShell 7')
class WindowsDownloadTests(unittest.TestCase):
    def test_download_and_checksum_contract_on_both_powershell_versions(self):
        # CI builds the real CLI first, so the success case also executes the
        # downloaded Windows binary. These runners do not establish SAC trust.
        payload = (ROOT / 'build/eigenflux.exe').read_bytes()
        expected = hashlib.sha256(payload).hexdigest().encode()
        for shell in ('powershell', 'pwsh'):
            self.assertIsNotNone(shutil.which(shell), f'{shell} is required')
            for case in ('valid', 'fresh', 'uppercase', 'missing', 'malformed', 'mismatch',
                         'checksum_server_error', 'retry', 'exhausted', 'without_checksum'):
                with self.subTest(shell=shell, case=case):
                    self.run_download(shell, case, payload, expected)

    def run_download(self, shell, case, payload, expected):
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                requests.append(self.path)
                if self.path.endswith('.sha256'):
                    status = {'missing': 404, 'checksum_server_error': 503}.get(case, 200)
                    body = {'malformed': b'<html>not a checksum</html>',
                            'mismatch': b'0' * 64,
                            'uppercase': expected.upper() + b'\r\n'}.get(case, expected + b'\n')
                else:
                    failed = case == 'exhausted' or (case == 'retry' and requests.count(self.path) == 1)
                    status, body = (503, b'unavailable') if failed else (200, payload)
                self.send_response(status)
                self.send_header('Content-Type', 'application/octet-stream')
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory(prefix='eigenflux Windows download ') as temp:
                directory = Path(temp)
                destination = directory / 'installed/eigenflux.exe'
                if case != 'fresh':
                    destination.parent.mkdir()
                    destination.write_bytes(b'previous installation')
                harness = directory / 'download.ps1'
                # Parse function declarations only; do not execute host setup,
                # PATH writes, provisioning or any other installer top-level code.
                harness.write_text('''$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$tokens = $null
$errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($env:INSTALLER_SOURCE, [ref]$tokens, [ref]$errors)
if ($errors.Count -ne 0) { throw ($errors | Out-String) }
foreach ($name in @('Info', 'Ok', 'Download-WithRetry')) {
    $function = $ast.Find({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name }, $true)
    if (-not $function) { throw "Missing installer function: $name" }
    Invoke-Expression $function.Extent.Text
}
Download-WithRetry -Url "$env:TEST_URL/eigenflux.exe" -Destination $env:TEST_DESTINATION -Sha256Url $env:TEST_CHECKSUM -MaxRetries 2
& $env:TEST_DESTINATION version --short
if ($LASTEXITCODE -ne 0) { throw 'Downloaded CLI did not execute successfully' }
''', encoding='utf-8-sig')
                url = f'http://127.0.0.1:{server.server_port}'
                env = dict(os.environ, TEMP=temp, TMP=temp,
                           INSTALLER_SOURCE=str(ROOT / 'static/install.ps1'),
                           TEST_URL=url, TEST_DESTINATION=str(destination),
                           TEST_CHECKSUM='' if case == 'without_checksum' else url + '/eigenflux.exe.sha256')
                # pwsh -> Python -> powershell otherwise inherits PS7 modules,
                # which can shadow the incompatible PS5.1 system modules.
                # Let each shell construct its own default module search path.
                env = {key: value for key, value in env.items() if key.upper() != 'PSMODULEPATH'}
                result = subprocess.run([shell, '-NoProfile', '-NonInteractive', '-File', str(harness)],
                                        env=env, capture_output=True, text=True, timeout=45)
                success = case in ('valid', 'fresh', 'uppercase', 'retry', 'without_checksum')
                self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
                if not success:
                    expected_error = {
                        'missing': '404', 'checksum_server_error': '503',
                        'malformed': 'Invalid SHA256 checksum',
                        'mismatch': 'SHA256 mismatch',
                        'exhausted': 'Download failed after 2 attempts',
                    }[case]
                    self.assertIn(expected_error, result.stdout + result.stderr)
                self.assertEqual(destination.read_bytes(), payload if success else b'previous installation')
                self.assertEqual(list(directory.glob('eigenflux-dl-*')), [])
                self.assertEqual(requests.count('/eigenflux.exe'), 2 if case in ('retry', 'exhausted') else 1)
                if case == 'without_checksum':
                    self.assertNotIn('/eigenflux.exe.sha256', requests)
                if success and case != 'without_checksum':
                    self.assertIn('SHA256 verified', result.stdout)
        finally:
            server.shutdown()
            thread.join()
            server.server_close()


if __name__ == '__main__':
    unittest.main()
