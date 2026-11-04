"""Run the actual build entry without compiling or contacting Apple."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class NotifierBuildEntryTests(unittest.TestCase):
    def test_independent_versions_reach_signing_validation(self):
        for cli_version, notifier_version, valid in [
            ('1.0.6', '1.0.5', True), ('1.0.5', '1.0.5', True),
            ('01.0.6', '1.0.5', False), ('1.0.6', 'invalid', False),
        ]:
            with self.subTest(cli=cli_version, notifier=notifier_version), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                scripts = root / 'cli/scripts'
                scripts.mkdir(parents=True)
                shutil.copy(ROOT / 'cli/scripts/build.sh', scripts / 'build.sh')
                (root / 'cli/.cli.config').write_text(
                    f'CLI_VERSION={cli_version}\nNOTIFIER_VERSION={notifier_version}\n')
                bindir = root / 'bin'
                bindir.mkdir()
                (bindir / 'go').write_text('#!/bin/sh\necho reached-signing-tool >&2\nexit 17\n')
                (bindir / 'go').chmod(0o755)
                env = dict(os.environ, PATH=str(bindir) + os.pathsep + os.environ['PATH'],
                           EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY='test',
                           EIGENFLUX_SKILLS_SIGNING_KEY_FILE='unused-test-key')
                result = subprocess.run(['bash', str(scripts / 'build.sh')],
                                        cwd=root, env=env, capture_output=True, text=True)
                if valid:
                    self.assertEqual(result.returncode, 17, result.stderr)
                    self.assertIn('reached-signing-tool', result.stderr)
                else:
                    self.assertEqual(result.returncode, 1, result.stderr)
                    self.assertIn('valid stable semantic version', result.stderr)
                    self.assertNotIn('reached-signing-tool', result.stderr)
