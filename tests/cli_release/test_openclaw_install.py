"""Exercise gateway compatibility through the full, isolated shell installer."""
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
CURRENT_PLUGIN = "@phronesis-io/openclaw-eigenflux"
LEGACY_PLUGIN = CURRENT_PLUGIN + "@0.0.8"


class OpenClawInstallation(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="eigenflux openclaw ")
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name)
        self.bin = self.home / "test bin"
        self.bin.mkdir()
        self.log = self.home / "calls"
        self.plugin = self.home / "openclaw-plugin"
        self.env = {
            "HOME": str(self.home), "PATH": str(self.bin) + ":/usr/bin:/bin",
            "EIGENFLUX_HOME": str(self.home / "agent/.eigenflux"),
            "EIGENFLUX_INSTALL_DIR": str(self.bin),
            "EIGENFLUX_CDN_URL": "https://cdn.invalid",
            "CODEX_HOME": str(self.home / "codex"),
            "TEST_CALLS": str(self.log), "TEST_PLUGIN": str(self.plugin),
            "TEST_OC_VERSION": "OpenClaw 2026.5.7 (test)",
        }
        self.stub("uname", 'if [ "$1" = -s ]; then echo Linux; else echo x86_64; fi')
        self.stub("curl", '''for arg do url="$arg"; done
case "$url" in
  */cli/v2/latest/version.txt) printf '\\n404' ;;
  */cli/latest/version.txt) echo 99.0.0 ;;
  *) echo "Unexpected network request: $url" >&2; exit 1 ;;
esac''')
        self.stub("eigenflux", '''printf 'eigenflux %s\\n' "$*" >> "$TEST_CALLS"
if [ "${1:-}" = version ]; then echo 99.0.0; fi''')
        self.stub("openclaw", '''printf 'openclaw %s\\n' "$*" >> "$TEST_CALLS"
case "$*" in
  --version) printf '%s\\n' "$TEST_OC_VERSION"; exit "${TEST_OC_EXIT:-0}" ;;
  'plugins list') if [ -f "$TEST_PLUGIN" ]; then echo eigenflux; fi ;;
  'plugins uninstall openclaw-eigenflux --force') rm -f "$TEST_PLUGIN" ;;
  'plugins install '*) printf '%s\\n' "$3" > "$TEST_PLUGIN" ;;
  'plugins update openclaw-eigenflux') printf '%s\\n' updated > "$TEST_PLUGIN" ;;
  'gateway restart') : ;;
  *) echo 'Unexpected OpenClaw operation' >&2; exit 1 ;;
esac''')
        self.stub("codex", '''printf 'codex %s\\n' "$*" >> "$TEST_CALLS"
case "$*" in
  --version) echo 'codex-cli 0.142.0' ;;
  'plugin list --json') if [ -f "$HOME/codex-present" ]; then echo '[{"id":"codex-eigenflux@eigenflux"}]'; else echo '[]'; fi ;;
  'plugin marketplace add phronesis-io/codex-eigenflux') : ;;
  'plugin add codex-eigenflux@eigenflux') touch "$HOME/codex-present" ;;
  *) echo 'Unexpected Codex operation' >&2; exit 1 ;;
esac''')
        self.stub("claude", 'echo "UNRELATED claude $*" >> "$TEST_CALLS"; exit 1')

    def stub(self, name, body):
        path = self.bin / name
        path.write_text("#!/bin/sh\nset -eu\n" + body + "\n")
        path.chmod(0o755)

    def install(self):
        self.log.write_text("")
        result = subprocess.run(
            ["sh", str(ROOT / "static/install.sh"), "--host", "openclaw"],
            env=self.env, cwd=self.home, stdin=subprocess.DEVNULL,
            capture_output=True, text=True, start_new_session=True, timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        calls = self.log.read_text()
        self.assertNotIn("UNRELATED", calls)
        self.assertNotIn("openclaw gateway restart", calls)
        receipts = [json.loads(line) for line in result.stdout.splitlines()
                    if line.startswith('{"component":"openclaw-eigenflux"')]
        return result, calls, receipts

    def test_current_gateway_uses_latest_at_manifest_minimum_and_later(self):
        for version in ("2026.5.7", "2026.5.8", "2026.6.0", "2027.1.0"):
            with self.subTest(version=version):
                self.plugin.unlink(missing_ok=True)
                self.env["TEST_OC_VERSION"] = f"OpenClaw {version} (test)"
                _, calls, receipts = self.install()
                self.assertEqual(self.plugin.read_text().strip(), CURRENT_PLUGIN)
                self.assertIn(f"openclaw plugins install {CURRENT_PLUGIN}\n", calls)
                self.assertEqual(receipts, [])

    def test_verified_legacy_gateway_keeps_pinned_release(self):
        for version in ("2026.3.1", "2026.3.13", "2026.4.0", "2026.5.0", "2026.5.1"):
            with self.subTest(version=version):
                self.plugin.write_text("existing plugin\n")
                self.env["TEST_OC_VERSION"] = version
                _, calls, receipts = self.install()
                self.assertEqual(self.plugin.read_text().strip(), LEGACY_PLUGIN)
                self.assertIn("openclaw plugins uninstall openclaw-eigenflux --force\n", calls)
                self.assertIn(f"openclaw plugins install {LEGACY_PLUGIN}\n", calls)
                self.assertEqual(receipts, [])

    def test_unsupported_boundaries_preserve_plugin_and_report_incomplete_setup(self):
        for version in ("2026.2.28", "2026.3.0", "2026.5.2", "2026.5.3", "2026.5.4", "2026.5.5", "2026.5.6"):
            with self.subTest(version=version):
                self.plugin.write_bytes(b"existing plugin\n")
                self.env["TEST_OC_VERSION"] = version
                result, calls, receipts = self.install()
                self.assertEqual(self.plugin.read_bytes(), b"existing plugin\n")
                self.assertNotIn("openclaw plugins ", calls)
                self.assertIn("Upgrade OpenClaw to 2026.5.7 or later", result.stderr)
                self.assertEqual(receipts, [{"component": "openclaw-eigenflux", "status": "skipped", "reason": "unsupported_gateway_version"}])

    def test_unknown_failed_and_prerelease_detection_never_guesses_latest(self):
        for version, exit_code in (("unknown", "0"), ("", "0"), ("2026.5.7", "1"),
                                   ("2026.5.7-rc.1", "0"), ("2026.05.7", "0"),
                                   ("2026.5.7rc.1", "0")):
            with self.subTest(version=version, exit_code=exit_code):
                self.env.update(TEST_OC_VERSION=version, TEST_OC_EXIT=exit_code)
                result, calls, receipts = self.install()
                self.assertNotIn("openclaw plugins ", calls)
                self.assertIn("Could not verify a stable OpenClaw version", result.stderr)
                self.assertEqual(receipts[0]["reason"], "unverified_gateway_version")

    def test_explicit_version_override_is_validated(self):
        self.env["OPENCLAW_VERSION"] = "2026.5.7"
        self.env["TEST_OC_VERSION"] = "unknown"
        _, calls, _ = self.install()
        self.assertNotIn("openclaw --version", calls)
        self.assertEqual(self.plugin.read_text().strip(), CURRENT_PLUGIN)
        for version in ("2026.5.7-rc.1", "2026.5.7; touch unsafe", "0.1.0"):
            with self.subTest(version=version):
                self.env["OPENCLAW_VERSION"] = version
                self.plugin.write_bytes(b"existing plugin\n")
                _, calls, receipts = self.install()
                self.assertNotIn("openclaw plugins ", calls)
                self.assertEqual(self.plugin.read_bytes(), b"existing plugin\n")
                self.assertEqual(receipts[0]["status"], "skipped")

    def test_skipped_openclaw_does_not_skip_another_requested_host(self):
        for version in ("2026.3.0", "2026.5.6", "unknown"):
            with self.subTest(version=version):
                (self.home / "codex-present").unlink(missing_ok=True)
                self.env.update(TEST_OC_VERSION=version, EIGENFLUX_SETUP_HOSTS="codex")
                _, calls, receipts = self.install()
                self.assertEqual(receipts[0]["status"], "skipped")
                self.assertIn("codex plugin add codex-eigenflux@eigenflux\n", calls)

    def test_current_gateway_updates_existing_plugin(self):
        self.plugin.write_text("existing plugin\n")
        _, calls, _ = self.install()
        self.assertIn("openclaw plugins update openclaw-eigenflux\n", calls)
        self.assertNotIn("openclaw plugins uninstall", calls)


if __name__ == "__main__":
    unittest.main()
