import importlib.util
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
def module(name):
    spec = importlib.util.spec_from_file_location(name, HERE / (name + ".py"))
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result

build, publish = module("build"), module("publish")


class Contract(unittest.TestCase):
    def test_publication_rejects_production_and_path_escape(self):
        for path in ["cli/latest", "skills/latest", "snapshots/../../cli/latest", "snapshots/x/y", "/snapshots/" + "a" * 40 + "/" + "b" * 40]:
            with self.assertRaises(ValueError):
                publish.validate_prefix(path)
        publish.validate_prefix(build.snapshot_prefix("b" * 40))

    def test_installer_keeps_host_setup_and_disables_mutable_fallback(self):
        original = (HERE.parents[1] / "static/install.sh").read_text()
        base = "https://cdn.example/snapshots/" + "a" * 40 + "/" + "b" * 40
        text = build.installer(original, base)
        self.assertIn('CDN_URL="' + base + '"', text)
        self.assertNotIn('CDN_URL="${EIGENFLUX_CDN_URL', text)
        self.assertNotIn("archive/refs/heads", text)
        self.assertIn('snapshot_sha256 "$INSTALLED_BIN"', text)
        self.assertIn('snapshot_sha256 "$TMP_FILE"', text)
        self.assertIn('return 1\n}', text)
        # The entire host/account setup region remains unchanged.
        start = "# ── Step 3: Migrate legacy config"
        tail = text[text.index(start):].replace('\ninstall_cli\nverify_snapshot_cli\n', '\ninstall_cli\n').replace('\nsetup_agents\nverify_snapshot_cli\ninstall_skills\n', '\nsetup_agents\n')
        self.assertEqual(original[original.index(start):], tail)

    def test_patch_refuses_changed_source_contract(self):
        with self.assertRaises(ValueError):
            build.replace_once("no match", "missing", "replacement")
        with self.assertRaises(ValueError):
            build.replace_once("x x", "x", "replacement")


if __name__ == "__main__":
    unittest.main(verbosity=2)
