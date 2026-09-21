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
        self.config = self.home / "custom codex/config.toml"
        self.config.parent.mkdir()
        self.original_config = b'model = "existing-model"\n[sandbox_workspace_write]\nnetwork_access = false\nwritable_roots = ["/existing/path"]\n'
        self.config.write_bytes(self.original_config)
        self.rules = self.config.parent / "rules/user.rules"
        self.rules.parent.mkdir()
        self.rules.write_bytes(b'prefix_rule(pattern=["other"], decision="prompt")\n')
        self.log = self.home / "calls"
        self.env = {
            "HOME": str(self.home), "CODEX_HOME": str(self.config.parent), "PATH": str(self.bin) + ":/usr/bin:/bin",
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
  '--version') echo "codex-cli ${TEST_CODEX_VERSION:-0.142.0}" ;;
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
        self.last_output = result.stdout + result.stderr
        for receipt in receipts:
            self.assertEqual(receipt["codex_path"], str(getattr(self, "expected_codex", self.bin / "codex")))
            self.assertEqual(receipt["codex_version"], getattr(self, "expected_version", "0.142.0"))
            self.assertEqual(receipt["codex_home"], str(self.config.parent))
            del receipt["codex_home"]
            # Existing status/activation contract remains unchanged.
            del receipt["codex_path"]
            del receipt["codex_version"]
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

    def test_old_path_falls_back_to_compatible_app_with_same_host_home(self):
        app = self.home / 'Applications/ChatGPT.app/Contents/Resources/codex'
        app.parent.mkdir(parents=True)
        app.write_text((self.bin / "codex").read_text().replace(
            "case \"$*\" in", 'test "$CODEX_HOME" = "${HOME}/custom codex" || exit 99\ncase "$*" in', 1))
        app.chmod(0o755)
        self.stub("codex", 'printf "old-codex %s\\n" "$*" >> "$TEST_CALLS"\nif [ "$*" = --version ]; then echo "codex-cli 0.139.0"; else exit 98; fi')
        self.expected_codex = app
        receipts, calls = self.install()
        self.assertEqual(receipts[0]["status"], "installed")
        self.assertIn("old-codex --version", calls)
        self.assertNotIn("old-codex plugin", calls)
        self.assertEqual(calls.count("codex plugin add codex-eigenflux@eigenflux\n"), 1)
        self.assertIn("Skipping incompatible Codex CLI", self.last_output)

    def selection(self, *candidates):
        # Invoke the production selector in isolation from machine-wide app paths.
        source = (ROOT / "static/install.sh").read_text()
        helpers = source[source.index("select_codex_cli() {"):source.index("setup_agents() {")]
        harness = self.home / "selection.sh"
        harness.write_text('info() { printf "%s\\n" "$1"; }\nerr() { printf "%s\\n" "$1" >&2; }\n' + helpers +
                           '\nselect_codex_cli "$@" || exit $?\ncodex_plugin_result installed restart_pending\n')
        return subprocess.run(["sh", str(harness), *map(str, candidates)], env=self.env,
                              cwd=self.home, capture_output=True, text=True, timeout=10)

    def test_optional_codex_discovery_preserves_explicit_requests(self):
        source = (ROOT / "static/install.sh").read_text()
        gate = source[source.index("note_skipped_host() {"):source.index("# ── Step 1:")]
        helpers = source[source.index("setup_codex_cli() {"):source.index("setup_agents() {")]
        harness = self.home / "discovery.sh"
        harness.write_text('set -eu\ninfo() { printf "%s\\n" "$1"; }\n'
                           'err() { printf "%s\\n" "$1" >&2; }\n' + gate + helpers +
                           '\nINVOKING_HOST="$1"; EIGENFLUX_SETUP_HOSTS="$2"; SKIPPED_HOSTS=""\n'
                           'shift 2\nsetup_codex_cli "$@" || exit $?\n'
                           'if [ -n "$CODEX_BIN" ]; then codex_plugin_result selected verify_in_host; fi\n')
        absent = self.home / "absent codex"
        cases = [
            ("", "", absent, "skip"),
            ("", "all", absent, "skip"),
            ("", "ALL", absent, "skip"),
            ("codex", "", absent, "failed"),
            ("", "claude-code,codex", absent, "failed"),
            ("claude-code", "", self.bin / "codex", "skip"),
            ("", "", self.bin / "codex", "selected"),
        ]
        for host, scope, candidate, expected in cases:
            with self.subTest(host=host, scope=scope, expected=expected):
                result = subprocess.run(["sh", str(harness), host, scope, "", str(candidate)],
                                        env=self.env, capture_output=True, text=True, timeout=10)
                if expected == "skip":
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout + result.stderr, "")
                else:
                    self.assertEqual(result.returncode, 1 if expected == "failed" else 0)
                    receipt = json.loads(result.stdout.splitlines()[-1])
                    self.assertEqual(receipt["status"], expected)
                    self.assertEqual(receipt["codex_home"], str(self.config.parent))
        self.env["TEST_CODEX_VERSION"] = "0.139.0"
        result = subprocess.run(["sh", str(harness), "", "", str(self.bin / "codex")],
                                env=self.env, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 1)
        self.assertIn("Upgrade Codex CLI", result.stderr)

    def test_only_old_unknown_or_failed_cli_reports_upgrade_without_plugin_calls(self):
        for version in ("0.139.0", "0.141.0", "0.142.0-alpha.1", "not-a-version"):
            with self.subTest(version=version):
                self.env["TEST_CODEX_VERSION"] = version
                result = self.selection(self.bin / "codex")
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Upgrade Codex CLI or the desktop app", result.stderr)
                receipt = json.loads(result.stdout.splitlines()[-1])
                self.assertEqual(receipt["status"], "failed")
                self.assertEqual(receipt["codex_path"], "")
                self.assertEqual(receipt["activation"], "unavailable")
        self.stub("codex", 'exit 1')
        self.assertNotEqual(self.selection(self.bin / "codex").returncode, 0)
        self.assertNotIn("codex plugin", self.log.read_text())

    def test_compatible_path_is_preferred_and_receipt_escapes_path(self):
        for version in ("0.142.0", "0.155.0-alpha.9.2", "1.0.0"):
            with self.subTest(version=version):
                self.env["TEST_CODEX_VERSION"] = version
                binary = self.bin / 'codex "quoted"'
                binary.write_bytes((self.bin / "codex").read_bytes())
                binary.chmod(0o755)
                result = self.selection(binary, "/does/not/exist")
                self.assertEqual(result.returncode, 0, result.stderr)
                receipt = json.loads(result.stdout.splitlines()[-1])
                self.assertEqual(receipt["codex_path"], str(binary))
                self.assertEqual(receipt["codex_version"], version)

    def test_non_codex_host_does_not_probe_codex_version(self):
        self.env["EIGENFLUX_SETUP_HOSTS"] = "terminal"
        # Explicit invoking host remains authoritative; test selector gating by
        # running the installer as a terminal, where Codex is unrelated.
        result = subprocess.run(["sh", str(ROOT / "static/install.sh"), "--host", "terminal"],
                                env=self.env, cwd=self.home, stdin=subprocess.DEVNULL,
                                capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("codex ", self.log.read_text())


if __name__ == "__main__":
    unittest.main()
