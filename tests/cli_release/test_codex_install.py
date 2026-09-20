"""Exercise the full installer with isolated host executables and configuration."""
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class CodexInstallation(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="eigenflux host ")
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name)
        self.bin = self.home / "test bin"
        self.bin.mkdir()
        self.config = self.home / ".codex/config.toml"
        self.config.parent.mkdir()
        self.original_config = b'model = "existing-model"\n[sandbox_workspace_write]\nnetwork_access = false\nwritable_roots = ["/existing/path"]\n'
        self.config.write_bytes(self.original_config)
        self.rules = self.config.parent / "rules/user.rules"
        self.rules.parent.mkdir()
        self.rules.write_bytes(b'prefix_rule(pattern=["other"], decision="prompt")\n')
        self.log = self.home / "calls"
        self.env = {
            "HOME": str(self.home), "PATH": str(self.bin) + ":/usr/bin:/bin",
            "EIGENFLUX_HOME": str(self.home / "explicit agent/.eigenflux"),
            "EIGENFLUX_CDN_URL": "https://cdn.invalid",
            "EIGENFLUX_INSTALL_DIR": str(self.bin),
            "TEST_CALLS": str(self.log), "TEST_PLUGIN": str(self.home / "plugin-present"),
        }
        self.stub("uname", 'if [ "$1" = -s ]; then echo Linux; else echo x86_64; fi')
        self.stub("curl", '''for arg do url="$arg"; done
case "$url" in
  */cli/latest/version.txt) echo 99.0.0 ;;
  *) echo "Unexpected network request: $url" >&2; exit 1 ;;
esac''')
        self.stub("eigenflux", '''printf 'eigenflux %s\\n' "$*" >> "$TEST_CALLS"
if [ "${1:-}" = version ]; then echo 99.0.0; fi''')
        self.stub("codex", '''printf 'codex %s\\n' "$*" >> "$TEST_CALLS"
case "$*" in
  'plugin list --json')
    if [ -f "$TEST_PLUGIN" ]; then echo '[{"id":"codex-eigenflux@eigenflux"}]'; else echo '[]'; fi ;;
  'plugin marketplace add phronesis-io/codex-eigenflux')
    if [ "${TEST_FAILURE:-}" = source ]; then echo 'already added from a different source' >&2; exit 1; fi ;;
  'plugin add codex-eigenflux@eigenflux')
    case "${TEST_FAILURE:-}" in
      add) echo 'host rejected install' >&2; exit 1 ;;
      missing) exit 0 ;;
      *) touch "$TEST_PLUGIN" ;;
    esac ;;
  'plugin marketplace upgrade eigenflux') : ;;
  *) echo 'unexpected Codex operation' >&2; exit 1 ;;
esac''')
        for host in ("openclaw", "claude"):
            self.stub(host, f'echo "UNRELATED {host} $*" >> "$TEST_CALLS"; exit 1')

    def stub(self, name, body):
        path = self.bin / name
        path.write_text("#!/bin/sh\nset -eu\n" + body + "\n")
        path.chmod(0o755)

    def install(self):
        result = subprocess.run(
            ["sh", str(ROOT / "static/install.sh"), "--host", "codex"],
            env=self.env, cwd=self.home, stdin=subprocess.DEVNULL,
            capture_output=True, text=True, start_new_session=True, timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.config.read_bytes(), self.original_config)
        self.assertEqual(self.rules.read_bytes(), b'prefix_rule(pattern=["other"], decision="prompt")\n')
        self.assertEqual(sorted(p.name for p in self.rules.parent.iterdir()), ["user.rules"])
        calls = self.log.read_text()
        self.assertNotIn("UNRELATED", calls)
        self.assertNotIn("agent provision", calls)
        self.assertNotIn("network_access", result.stdout)
        self.assertNotIn("writable_roots", result.stdout)
        self.assertNotIn("reopen", result.stdout.lower())
        self.assertNotIn("new task", result.stdout.lower())
        self.assertIn("explicit agent/.eigenflux", calls)
        receipts = [json.loads(line) for line in result.stdout.splitlines()
                    if line.startswith('{"component":"codex-eigenflux"')]
        return receipts, calls

    def test_fresh_install_reports_pending_activation_without_early_restart(self):
        receipts, calls = self.install()
        self.assertEqual(receipts, [{"component": "codex-eigenflux", "status": "installed", "activation": "restart_pending"}])
        self.assertEqual(calls.count("codex plugin add codex-eigenflux@eigenflux\n"), 1)

    def test_reinstall_reuses_plugin_without_claiming_running_activation(self):
        self.install()
        receipts, calls = self.install()
        self.assertEqual(receipts, [{"component": "codex-eigenflux", "status": "present", "activation": "verify_in_host"}])
        self.assertEqual(calls.count("codex plugin add codex-eigenflux@eigenflux\n"), 1)

    def test_missing_sandbox_section_is_not_added(self):
        self.original_config = b'model = "existing-model"\n'
        self.config.write_bytes(self.original_config)
        self.install()

    def test_failures_do_not_masquerade_as_installed_or_pending_activation(self):
        for failure in ("source", "add", "missing"):
            with self.subTest(failure=failure):
                self.log.write_text("")
                self.env["TEST_FAILURE"] = failure
                receipts, calls = self.install()
                self.assertEqual(receipts, [{"component": "codex-eigenflux", "status": "failed", "activation": "unavailable"}])
                self.assertFalse(Path(self.env["TEST_PLUGIN"]).exists())
                if failure == "source":
                    self.assertNotIn("codex plugin add ", calls)

    def test_explicit_host_setup_opt_out_preserves_host(self):
        self.env["EIGENFLUX_SKIP_AGENT_SETUP"] = "1"
        receipts, calls = self.install()
        self.assertEqual(receipts, [])
        self.assertNotIn("codex ", calls)


if __name__ == "__main__":
    unittest.main()
