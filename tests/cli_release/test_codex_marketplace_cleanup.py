"""Opt-in native Codex cleanup/reinstall regression; all state and Git are local."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


@unittest.skipUnless(os.environ.get("EIGENFLUX_TEST_CODEX"), "set EIGENFLUX_TEST_CODEX to a native Codex CLI")
class CodexMarketplaceCleanup(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="ef marketplace cleanup ")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.codex = str(Path(os.environ["EIGENFLUX_TEST_CODEX"]).resolve(strict=True))
        self.home = self.root / "host home"
        self.home.mkdir()
        self.env = {
            "HOME": str(self.home), "USERPROFILE": str(self.home),
            "CODEX_HOME": str(self.home / ".codex"),
            "PATH": os.pathsep.join([str(Path(shutil.which("git")).parent), "/usr/bin", "/bin"]),
            "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_TERMINAL_PROMPT": "0",
        }
        self.source = self.root / "official fixture"
        self.source.mkdir()
        manifest = self.source / ".agents/plugins/marketplace.json"
        manifest.parent.mkdir(parents=True)
        manifest.write_text(json.dumps({"name": "eigenflux", "plugins": [{
            "name": "codex-eigenflux", "source": {"source": "local", "path": "."},
            "policy": {"installation": "AVAILABLE", "authentication": "ON_USE"},
            "category": "Productivity",
        }]}))
        plugin = self.source / ".codex-plugin/plugin.json"
        plugin.parent.mkdir()
        plugin.write_text(json.dumps({"name": "codex-eigenflux", "version": "0.0.1"}))
        for args in (["init"], ["add", "."], ["-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture"]):
            subprocess.run(["git", *args], cwd=self.source, env=self.env,
                           check=True, capture_output=True, timeout=15)
        # Download no external code. Redirect the canonical source to this local
        # catalog; allow only file transport so a missed rewrite fails closed.
        self.env.update({
            "GIT_CONFIG_COUNT": "1",
            "GIT_CONFIG_KEY_0": "url." + self.source.as_uri() + ".insteadOf",
            "GIT_CONFIG_VALUE_0": "https://github.com/phronesis-io/codex-eigenflux.git",
            "GIT_ALLOW_PROTOCOL": "file",
        })
        self.config = self.home / ".codex/config.toml"
        self.config.parent.mkdir()
        self.unrelated = 'model = "unchanged-model"\n[plugins."other@other"]\nenabled = false\n'
        self.config.write_text(self.unrelated)
        self.market = self.config.parent / ".tmp/marketplaces/eigenflux"
        self.other = self.config.parent / ".tmp/marketplaces/other/keep.txt"
        self.other.parent.mkdir(parents=True)
        self.other.write_text("unrelated marketplace")

    def call(self, *args, success=True):
        result = subprocess.run([self.codex, "plugin", "marketplace", *args],
                                cwd=self.root, env=self.env, capture_output=True,
                                text=True, timeout=30)
        if success:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def verify_remove_and_readd(self):
        self.call("remove", "eigenflux")
        self.assertFalse(self.market.exists())
        self.assertNotIn("[marketplaces.eigenflux]", self.config.read_text())
        self.assertIn('model = "unchanged-model"', self.config.read_text())
        self.assertIn('[plugins."other@other"]', self.config.read_text())
        self.assertEqual(self.other.read_text(), "unrelated marketplace")
        self.call("add", "phronesis-io/codex-eigenflux")
        self.assertIn("eigenflux", self.call("list").stdout)
        self.assertTrue((self.market / ".agents/plugins/marketplace.json").is_file())

    def test_registered_marketplace_removal_clears_installed_root(self):
        self.call("add", "phronesis-io/codex-eigenflux")
        self.verify_remove_and_readd()

    def test_orphan_hidden_from_list_still_needs_native_marketplace_removal(self):
        self.call("add", "phronesis-io/codex-eigenflux")
        # Reproduce removal of configuration without its installed checkout.
        self.config.write_text(self.unrelated)
        self.assertNotIn("eigenflux", self.call("list").stdout)
        rejected = self.call("add", "phronesis-io/codex-eigenflux", success=False)
        self.assertNotEqual(rejected.returncode, 0)
        self.assertIn("different source", rejected.stderr)
        self.verify_remove_and_readd()


if __name__ == "__main__":
    unittest.main()
