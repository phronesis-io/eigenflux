"""Exercise the real installer with a local OSS command fixture."""
import configparser
import hashlib
import json
import os
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
                     "basename", "cp", "rm", "mv", "readlink", "python3"):
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
        files = ["model.json"]
        if version.startswith("gbdt_"):
            (bundle / "report.json").write_text('{"promotion_passed": true}')
            files.append("report.json")
        (bundle / "checksums.sha256").write_text("".join(
            f"{hashlib.sha256((bundle / name).read_bytes()).hexdigest()}  {name}\n"
            for name in files
        ))
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
        self.check_latest("lr_20261002_0700_2434cdb")

    def test_discovery_gbdt_activation(self):
        self.env["OSS_MODEL_PREFIX"] = "rec/model/discovery_gbdt"
        self.check_latest("gbdt_20261002_0700_2434cdb")

    def test_publisher_success_triggers_isolated_gbdt_pull(self):
        units = SCRIPT.parents[2] / "cloud/systemd"
        trigger = configparser.ConfigParser()
        trigger.read(units / "eigenflux-discovery-gbdt-train.service.d/model-pull.conf")
        pull = units / trigger["Unit"]["OnSuccess"]
        self.assertEqual(pull.name, "eigenflux-discovery-gbdt-model-pull.service")
        lines = pull.read_text().splitlines()
        self.assertIn("Environment=MODEL_ROOT=/data/models/eigenflux/discovery-gbdt", lines)
        self.assertIn("Environment=OSS_MODEL_PREFIX=rec/model/discovery_gbdt", lines)
        command = next(line for line in lines if line.startswith("ExecStart="))
        _, args = command.split("/scripts/cloud/install_lr_model.sh ")
        self.assertEqual(args.split(), ["--oss-latest", "gbdt"])
        self.check_latest("gbdt_20261002_0700_2434cdb", "gbdt")

    def check_latest(self, version, *args):
        self.stub_latest(self.make_bundle(version), version)
        result = self.run_command("--oss-latest", *args)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.root / "models/current").resolve().name, version)
        self.assertIn("sample_date=2026-10-01/" + version, result.stdout)

    def stub_latest(self, bundle, version):
        self.env["FIXTURE_BUNDLE"] = str(bundle)
        self.env["FIXTURE_VERSION"] = version
        oss = self.bin / "ossutil"
        oss.write_text('''#!/bin/bash
case "$3" in
ls)
  if [[ "$4" == *sample_date=* ]]; then
    for version in "$FIXTURE_VERSION" lr_20250101_0100_1 gbdt_20250101_0100_1; do
      echo "${4}${version}/"
    done
    echo "${4}${FIXTURE_VERSION}_partial/"
    echo "${4}old_${FIXTURE_VERSION}/"
    # A newer bundle of the other family is also present.
    if [[ "$FIXTURE_VERSION" == gbdt_* ]]; then other=lr; else other=gbdt; fi
    echo "${4}${other}_20990101_0100_1/"
  else
    echo "${4}sample_date=2026-10-01/"
    echo "${4}sample_date=2026-09-30/"
  fi
  ;;
cp)
  [[ "$6" == */sample_date=2026-10-01/"$FIXTURE_VERSION"/ ]] || exit 8
  cp -R "$FIXTURE_BUNDLE/." "$7"
  ;;
*) exit 9 ;;
esac
''')
        oss.chmod(0o755)

    def test_explicit_gbdt_family_and_rollback(self):
        self.env["OSS_MODEL_PREFIX"] = "alternate/published"
        first = "gbdt_20261001_0700_aaaa"
        second = "gbdt_20261002_0700_bbbb"
        self.check_latest(first, "gbdt")
        self.check_latest(second, "gbdt")
        models = Path(self.env["MODEL_ROOT"])
        self.assertEqual((models / "previous").resolve().name, first)

        self.assertEqual(self.run_command("--oss-latest", "gbdt").returncode, 0)
        result = self.run_command("--rollback")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((models / "current").resolve().name, first)
        self.assertEqual((models / "previous").resolve().name, second)

    def test_failed_gbdt_publication_preserves_links(self):
        first = "gbdt_20261001_0700_aaaa"
        second = "gbdt_20261002_0700_bbbb"
        self.check_latest(first, "gbdt")
        self.check_latest(second, "gbdt")
        models = Path(self.env["MODEL_ROOT"])
        bundle = self.make_bundle("gbdt_20261003_0700_cccc")
        self.stub_latest(bundle, "gbdt_20261004_0700_dddd")
        result = self.run_command("--oss-latest", "gbdt")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not match expected version", result.stderr)
        self.env["FIXTURE_VERSION"] = "gbdt_20261003_0700_cccc"
        manifest = bundle / "checksums.sha256"
        original = manifest.read_text()
        for contents, error in ((original.splitlines()[0] + "\n", "missing checksum"),
                                (original, "checksum verification failed")):
            (bundle / "report.json").write_text("incomplete")
            manifest.write_text(contents)
            result = self.run_command("--oss-latest", "gbdt")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(error, result.stderr)
        self.assertEqual((models / "current").resolve().name, second)
        self.assertEqual((models / "previous").resolve().name, first)
        self.assertCountEqual((p.name for p in (models / "versions").iterdir()), [first, second])

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
