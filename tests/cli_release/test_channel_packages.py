"""Channel archives must use the current installer and onboarding entry."""
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "channel_packages", ROOT / "scripts/release/build-channel-packages.py"
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class ChannelPackagesTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.output = Path(self.temp.name) / "output"
        self.packages = MODULE.build(self.output)

    def test_archives_contain_current_entry_and_exact_official_installer(self):
        expected_files = {
            "README.md", "SKILL.md", "scripts/install-eigenflux.sh", "scripts/install.sh"
        }
        for package in self.packages:
            with self.subTest(package=package.name):
                actual = {str(p.relative_to(package)) for p in package.rglob("*") if p.is_file()}
                self.assertEqual(actual, expected_files)
                self.assertEqual(
                    (package / "scripts/install.sh").read_bytes(),
                    (ROOT / "static/install.sh").read_bytes(),
                )
                skill = (package / "SKILL.md").read_text()
                self.assertIn("ef-onboarding", skill)
                self.assertIn("verify and\nbind their email", skill)
                self.assertNotIn("auth login", skill)
                self.assertNotIn("ef-trading", skill)
                for script in (package / "scripts").iterdir():
                    subprocess.run(["sh", "-n", str(script)], check=True)
                with zipfile.ZipFile(package.with_suffix(".zip")) as archive:
                    self.assertEqual(
                        set(archive.namelist()),
                        {f"{package.name}/{name}" for name in expected_files},
                    )
                    for name in expected_files:
                        self.assertEqual(archive.read(f"{package.name}/{name}"), (package / name).read_bytes())

    def invoke_wrapper(self, channel, args=(), referral=None):
        package = self.output / MODULE.CHANNELS[channel][0]
        installer = package / "scripts/install.sh"
        installer.write_text('#!/bin/sh\nprintf "%s\\n" "$@"\n')
        env = os.environ.copy()
        env.pop("EF_REF_CODE", None)
        if referral is not None:
            env["EF_REF_CODE"] = referral
        return subprocess.run(
            ["sh", str(package / "scripts/install-eigenflux.sh"), *args],
            env=env, text=True, capture_output=True,
        )

    def test_wrapper_preserves_channel_host_home_and_explicit_override(self):
        args = ["--host", "workbuddy", "--homedir", "/tmp/home with spaces", "--ref", "EF-Abcd1234"]
        result = self.invoke_wrapper("xhs", args)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), ["--ref", "EF-1PoI4QXu", *args])

    def test_public_channel_does_not_invent_referral(self):
        result = self.invoke_wrapper("skillhub", ["--host", "workbuddy"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), ["--host", "workbuddy"])

    def test_referral_env_overrides_channel_default(self):
        result = self.invoke_wrapper("xhs", referral="EF-2G5bp7G1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), ["--ref", "EF-2G5bp7G1"])

    def test_invalid_referral_stops_before_installer(self):
        result = self.invoke_wrapper("xhs", referral="invalid;echo unsafe")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertIn("Invalid", result.stderr)

    def test_existing_output_is_never_replaced(self):
        sentinel = self.packages[0] / "owner.txt"
        sentinel.write_text("keep")
        with self.assertRaises(FileExistsError):
            MODULE.build(self.output)
        self.assertEqual(sentinel.read_text(), "keep")


if __name__ == "__main__":
    unittest.main()
