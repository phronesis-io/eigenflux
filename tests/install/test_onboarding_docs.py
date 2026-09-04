"""Offline installer overlay checks; no real installation or network calls."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class OnboardingDocsTest(unittest.TestCase):
    def run_overlay(self, fail=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / 'bin'
            binary.mkdir()
            profile = root / 'skills/ef-profile'
            (profile / 'references').mkdir(parents=True)
            (profile / 'SKILL.md').write_text('released entry')
            (profile / 'references/onboarding-v2.md').write_text('released reference')
            (profile / 'references/config.md').write_text('keep other docs')
            cli = binary / 'eigenflux'
            cli.write_text('#!/bin/sh\nprintf "%s\\n" "$TEST_ROOT/skills"\n')
            curl = binary / 'curl'
            curl.write_text('''#!/bin/sh
case "$2" in
  https://raw.githubusercontent.com/phronesis-io/eigenflux/codex/onboarding-prefill-consent-main/skills/ef-profile/*) ;;
  *) exit 8 ;;
esac
case "$2" in
  */references/onboarding-v2.md)
    [ "$FAIL_DOWNLOAD" != 1 ] || exit 22
    printf 'test reference' > "$4" ;;
  */SKILL.md) printf 'test entry' > "$4" ;;
  *) exit 9 ;;
esac
''')
            cli.chmod(0o755)
            curl.chmod(0o755)
            env = dict(os.environ, PATH=str(binary)+':'+os.environ['PATH'],
                       TEST_ROOT=str(root), FAIL_DOWNLOAD='1' if fail else '0',
                       EIGENFLUX_INSTALL_DIR=str(binary), EIGENFLUX_INSTALLER_TEST_MODE='1')
            result = subprocess.run(['sh', '-c', '. "$1"; install_onboarding_test_docs',
                                     'test', str(ROOT / 'static/install.sh'), '--host', 'codex'],
                                    env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode == 0, not fail, result.stderr)
            self.assertEqual((profile / 'SKILL.md').read_text(),
                             'released entry' if fail else 'test entry')
            self.assertEqual((profile / 'references/onboarding-v2.md').read_text(),
                             'released reference' if fail else 'test reference')
            self.assertEqual((profile / 'references/config.md').read_text(), 'keep other docs')

    def test_restores_both_documents(self):
        self.run_overlay()

    def test_failed_download_stops_without_partial_overlay(self):
        self.run_overlay(fail=True)


if __name__ == '__main__':
    unittest.main()
