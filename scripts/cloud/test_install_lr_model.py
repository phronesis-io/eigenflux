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


if __name__ == "__main__":
    unittest.main()
