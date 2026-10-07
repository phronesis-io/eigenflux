"""Exercise the real installer with a local OSS command fixture."""
import os
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("install_lr_model.sh")


class InstallerFailures(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        for name in ("grep", "tr", "sort", "tail", "mkdir", "mktemp", "find",
                     "dirname", "cp", "rm", "mv", "readlink", "python3"):
            (self.bin / name).symlink_to(shutil.which(name))
        checksum = self.bin / "sha256sum"
        if shutil.which("sha256sum"):
            checksum.symlink_to(shutil.which("sha256sum"))
        else:
            checksum.write_text('#!/bin/bash\nexec /usr/bin/shasum -a 256 "$@"\n')
            checksum.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.bin), MODEL_ROOT=str(self.root / "models"))

    def make_bundle(self, version):
        bundle = self.root / ("bundle-" + version)
        bundle.mkdir()
        model = json.dumps({"model_version": version}).encode()
        (bundle / "model.json").write_bytes(model)
        (bundle / "checksums.sha256").write_text(hashlib.sha256(model).hexdigest() + "  model.json\n")
        return bundle

    def run_command(self, *args):
        return subprocess.run(["/bin/bash", str(SCRIPT), *map(str, args)],
                              env=self.env, text=True, capture_output=True)

    def install_bundle(self, bundle):
        result = self.run_command("--src", bundle)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def run_installer(self, oss=None):
        if oss is not None:
            path = self.bin / "ossutil"
            path.write_text("#!/bin/bash\n" + oss)
            path.chmod(0o755)
        result = subprocess.run(["/bin/bash", str(SCRIPT), "--oss-latest"],
                                env=self.env, text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / "models").exists())
        return result.stdout + result.stderr

    def test_missing_tool_is_not_reported_as_empty_bucket(self):
        output = self.run_installer()
        self.assertIn("ossutil not found", output)
        self.assertNotIn("no sample_date=", output)

    def test_listing_failure_preserves_cause_even_with_partial_output(self):
        output = self.run_installer('echo "oss://eigenflux/rec/model/lr/sample_date=2026-10-01/"\necho AccessDenied >&2\nexit 2\n')
        self.assertIn("AccessDenied", output)
        self.assertIn("failed to list", output)
        self.assertNotIn("no sample_date=", output)

    def test_version_listing_failure_preserves_cause(self):
        output = self.run_installer('if [[ "$4" == *sample_date=* ]]; then echo NetworkError >&2; exit 2; fi\necho "oss://eigenflux/rec/model/lr/sample_date=2026-10-01/"\n')
        self.assertIn("NetworkError", output)
        self.assertIn("failed to list", output)
        self.assertNotIn("no lr_*", output)

    def test_successful_empty_listing_has_distinct_error(self):
        output = self.run_installer('exit 0\n')
        self.assertIn("no sample_date=", output)

    def test_successful_listing_download_verification_and_activation(self):
        version = "lr_20261002_0700_2434cdb"
        bundle = self.root / "bundle"
        bundle.mkdir()
        model = json.dumps({"model_version": version}).encode()
        (bundle / "model.json").write_bytes(model)
        (bundle / "checksums.sha256").write_text(hashlib.sha256(model).hexdigest() + "  model.json\n")
        self.env["FIXTURE_BUNDLE"] = str(bundle)
        oss = self.bin / "ossutil"
        oss.write_text('''#!/bin/bash
case "$3" in
ls)
  if [[ "$4" == *sample_date=* ]]; then
    echo "oss://eigenflux/rec/model/lr/sample_date=2026-10-01/lr_20261002_0700_2434cdb/"
  else
    echo "oss://eigenflux/rec/model/lr/sample_date=2026-09-30/"
    echo "oss://eigenflux/rec/model/lr/sample_date=2026-10-01/"
  fi
  ;;
cp) cp -R "$FIXTURE_BUNDLE/." "$7" ;;
*) exit 9 ;;
esac
''')
        oss.chmod(0o755)
        result = subprocess.run(["/bin/bash", str(SCRIPT), "--oss-latest"],
                                env=self.env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.root / "models/current").resolve().name, version)
        self.assertIn("sample_date=2026-10-01/" + version, result.stdout)

    def test_repeat_install_preserves_previous_and_rollback(self):
        first = self.make_bundle("lr_20261001_0700_aaaa")
        second = self.make_bundle("lr_20261002_0700_bbbb")
        self.install_bundle(first)
        self.install_bundle(second)
        models = Path(self.env["MODEL_ROOT"])
        previous = (models / "previous").readlink()
        self.install_bundle(second)
        self.assertEqual((models / "previous").readlink(), previous)
        result = self.run_command("--rollback")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((models / "current").resolve().name, "lr_20261001_0700_aaaa")
        self.assertEqual((models / "previous").resolve().name, "lr_20261002_0700_bbbb")

    def test_first_version_repeat_does_not_create_previous(self):
        bundle = self.make_bundle("lr_20261001_0700_aaaa")
        self.install_bundle(bundle)
        self.install_bundle(bundle)
        models = Path(self.env["MODEL_ROOT"])
        self.assertEqual((models / "current").resolve().name, "lr_20261001_0700_aaaa")
        self.assertFalse(os.path.lexists(models / "previous"))

    def test_repeat_install_compares_canonical_targets(self):
        first = self.make_bundle("lr_20261001_0700_aaaa")
        second = self.make_bundle("lr_20261002_0700_bbbb")
        for spelling in ("relative-current", "aliased-root"):
            with self.subTest(spelling=spelling):
                models = self.root / spelling
                self.env["MODEL_ROOT"] = str(models)
                self.install_bundle(first)
                self.install_bundle(second)
                previous = (models / "previous").readlink()
                current = models / "current"
                if spelling == "relative-current":
                    current.unlink()
                    current.symlink_to("versions/lr_20261002_0700_bbbb")
                else:
                    alias = self.root / "model-root-alias"
                    alias.symlink_to(models, target_is_directory=True)
                    self.env["MODEL_ROOT"] = str(alias)
                current_target = current.readlink()
                self.install_bundle(second)
                self.assertEqual(current.readlink(), current_target)
                self.assertEqual((models / "previous").readlink(), previous)

    def test_repeat_install_still_rejects_corrupt_bundles(self):
        first = self.make_bundle("lr_20261001_0700_aaaa")
        second = self.make_bundle("lr_20261002_0700_bbbb")
        for corrupted in ("staged", "installed"):
            with self.subTest(corrupted=corrupted):
                models = self.root / corrupted
                self.env["MODEL_ROOT"] = str(models)
                self.install_bundle(first)
                self.install_bundle(second)
                previous = (models / "previous").readlink()
                current = (models / "current").readlink()
                path = second / "model.json" if corrupted == "staged" else models / "current/model.json"
                original = path.read_bytes()
                path.write_text("corrupt bundle\n")
                result = self.run_command("--src", second)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("checksum verification failed", result.stdout + result.stderr)
                self.assertEqual((models / "current").readlink(), current)
                self.assertEqual((models / "previous").readlink(), previous)
                path.write_bytes(original)


if __name__ == "__main__":
    unittest.main()
