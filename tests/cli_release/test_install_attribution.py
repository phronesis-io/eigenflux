"""Run the actual installer attribution functions with isolated curl responses."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SOURCE = (ROOT / "static/install.sh").read_text()
FUNCTIONS = SOURCE[SOURCE.index("install_report_response_ok() {"):SOURCE.index("# ── Main")]


class InstallAttribution(unittest.TestCase):
    def run_report(self, responses, ref="EF-12345678"):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "responses.json").write_text(json.dumps(responses))
            (root / "curl").write_text("#!/usr/bin/env python3\n" + """
import json, os, pathlib, sys
root = pathlib.Path(os.environ['REPORT_TEST_DIR'])
log = root / 'calls'
calls = log.read_text().splitlines() if log.exists() else []
with log.open('a') as stream: stream.write(json.dumps(sys.argv[1:]) + '\\n')
responses = json.loads((root / 'responses.json').read_text())
code, body, status = responses[min(len(calls), len(responses)-1)]
sys.stdout.write(body + '\\n' + code)
sys.exit(status)
""")
            (root / "curl").chmod(0o755)
            script = "set -e\ninfo(){ printf '%s\\n' \"$1\"; }\nok(){ printf '%s\\n' \"$1\"; }\nsleep(){ :; }\n" + FUNCTIONS
            script += '\nreport_attribution\nprintf "continued=yes\\n"\n'
            environment = dict(os.environ, REPORT_TEST_DIR=directory,
                               INSTALL_REF=ref, EIGENFLUX_API_URL="https://example.invalid",
                               PATH=directory + os.pathsep + os.environ["PATH"])
            result = subprocess.run(["sh", "-c", script], env=environment,
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("continued=yes", result.stdout)
            log = root / "calls"
            calls = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
            for call in calls:
                self.assertEqual(call[call.index("--connect-timeout") + 1], "5")
                self.assertEqual(call[call.index("--max-time") + 1], "15")
                self.assertEqual(json.loads(call[call.index("-d") + 1])["ref"], ref)
            return result.stdout, len(calls)

    def test_empty_and_malformed_refs_do_not_request(self):
        for ref in ("", "EFI-123456", "EF-bad", "EF-12345678;echo bad"):
            with self.subTest(ref=ref):
                _, calls = self.run_report([], ref)
                self.assertEqual(calls, 0)

    def test_success_accepts_valid_top_level_code(self):
        for body in ('{"code":0}', '{"msg":"ok", "data":{"code":3}, "code":0}',
                     '{\n"code": 0, "msg":"escaped \\\" quote", "data":[true,null,-1.2e3]}'):
            with self.subTest(body=body):
                output, calls = self.run_report([("200", body, 0)])
                self.assertEqual(calls, 1)
                self.assertIn("Install attributed", output)

    def test_bad_success_bodies_never_claim_success(self):
        for body in ('', '<html>ok</html>', '{"code":1}', '{"code":"0"}',
                     '{"data":{"code":0}}', '{"code":0,garbage}',
                     '{"code":0,"code":1}', '{"code":0} trailing',
                     '{"msg":"\\q", "code":0}', '{"code":0,"data":[1,]}'):
            with self.subTest(body=body):
                output, calls = self.run_report([("200", body, 0)])
                self.assertEqual(calls, 3)
                self.assertNotIn("Install attributed", output)
                self.assertIn("reporting is incomplete", output)

    def test_transport_failure_retries_without_concatenating_http_code(self):
        output, calls = self.run_report([("000", "", 28), ("200", '{"code":0}', 0)])
        self.assertEqual(calls, 2)
        self.assertIn("Install attributed", output)

    def test_retryable_http_statuses_recover(self):
        for code in ("408", "429", "500", "502", "503"):
            with self.subTest(code=code):
                output, calls = self.run_report([(code, '{}', 0), ("200", '{"code":0}', 0)])
                self.assertEqual(calls, 2)
                self.assertIn("Install attributed", output)

    def test_permanent_http_errors_stop_after_one_attempt(self):
        for code in ("400", "401", "403", "404", "422", "302"):
            with self.subTest(code=code):
                output, calls = self.run_report([(code, '{}', 0)])
                self.assertEqual(calls, 1)
                self.assertIn("reporting is incomplete", output)

    def test_retry_budget_is_three_attempts(self):
        output, calls = self.run_report([("503", '{}', 0)])
        self.assertEqual(calls, 3)
        self.assertIn("CLI installation succeeded", output)


if __name__ == "__main__":
    unittest.main()
