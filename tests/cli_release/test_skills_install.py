"""Exercise GitHub Skills fallback through the complete public installer."""
import io
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class SkillsInstallation(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="eigenflux skills ")
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name)
        self.bin = self.home / "bin"
        self.bin.mkdir()
        self.skills = self.home / ".agents/skills"
        self.archive = self.home / "source.tar.gz"
        self.env = {
            "HOME": str(self.home), "PATH": str(self.bin) + ":/usr/bin:/bin",
            "EIGENFLUX_INSTALL_DIR": str(self.bin),
            "EIGENFLUX_HOME": str(self.home / "agent/.eigenflux"),
            "EIGENFLUX_SKIP_AGENT_SETUP": "1", "TEST_VERSION": "0.0.51",
            "TEST_SKILLS": str(self.skills), "TEST_ARCHIVE": str(self.archive),
        }
        self.stub("curl", '''for arg do url="$arg"; done
case "$url" in
  */cli/latest/version.txt) printf '%s\\n' "$TEST_VERSION" ;;
  */archive/refs/heads/main.tar.gz) cat "$TEST_ARCHIVE" ;;
  *) echo "Unexpected network request: $url" >&2; exit 1 ;;
esac''')
        self.stub("eigenflux", '''case "$*" in
  'version --short') printf '%s\\n' "$TEST_VERSION" ;;
  'skills sync --host codex') echo 'skills need a newer CLI' >&2; exit 1 ;;
  'skills path --host codex') printf '%s\\n' "$TEST_SKILLS" ;;
  *) : ;;
esac''')

    def stub(self, name, body):
        path = self.bin / name
        path.write_text("#!/bin/sh\nset -eu\n" + body + "\n")
        path.chmod(0o755)

    def source_archive(self, minimum):
        files = {"skills/ef-onboarding/SKILL.md": b"new onboarding"}
        if minimum is not None:
            files["cli/.cli.config"] = f"SKILLS_MIN_CLI_VERSION={minimum}\n".encode()
        with tarfile.open(self.archive, "w:gz") as archive:
            for path, content in files.items():
                info = tarfile.TarInfo("eigenflux-main/" + path)
                info.size = len(content)
                archive.addfile(info, io.BytesIO(content))

    def install(self):
        return subprocess.run(
            ["sh", str(ROOT / "static/install.sh"), "--host", "codex"],
            env=self.env, cwd=self.home, stdin=subprocess.DEVNULL,
            capture_output=True, text=True, start_new_session=True, timeout=30,
        )

    def test_incompatible_fallback_stops_fresh_install(self):
        self.source_archive("0.0.52")
        result = self.install()
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("0.0.52", result.stderr)
        self.assertFalse(self.skills.exists())
        self.assertNotIn("Done!", result.stdout)

    def test_incompatible_fallback_preserves_existing_skills(self):
        self.source_archive("0.0.53")
        existing = self.skills / "ef-onboarding/SKILL.md"
        existing.parent.mkdir(parents=True)
        existing.write_text("existing onboarding")
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(existing.read_text(), "existing onboarding")
        self.assertFalse((self.skills / ".ef-stale").exists())

    def test_compatible_fallback_keeps_bootstrap_available(self):
        self.source_archive("0.0.52")
        for version in ("0.0.52", "0.0.53", "0.1.0", "1.0.0"):
            with self.subTest(version=version):
                self.env["TEST_VERSION"] = version
                result = self.install()
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual((self.skills / "ef-onboarding/SKILL.md").read_text(), "new onboarding")
                self.assertTrue((self.skills / ".ef-stale").exists())

    def test_unknown_compatibility_does_not_copy_skills(self):
        for minimum in (None, "", "unknown", "0.0.52;touch unwanted"):
            with self.subTest(minimum=minimum):
                self.source_archive(minimum)
                result = self.install()
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.skills.exists())
                self.assertFalse((self.home / "unwanted").exists())


if __name__ == "__main__":
    unittest.main()
