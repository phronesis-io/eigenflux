"""Old macOS clients validate the helper before executing the incoming CLI."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class NotifierCompatibilityTests(unittest.TestCase):
    def test_mismatched_version_stops_before_compilation(self):
        with tempfile.TemporaryDirectory() as directory:
            cli = Path(directory) / 'cli'
            scripts = cli / 'scripts'
            scripts.mkdir(parents=True)
            script = scripts / 'build.sh'
            script.write_text((ROOT / 'cli/scripts/build.sh').read_text())
            (cli / '.cli.config').write_text('CLI_VERSION=1.0.2\nNOTIFIER_VERSION=1.0.0\n')
            result = subprocess.run(['bash', str(script)], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('NOTIFIER_VERSION must match CLI_VERSION', result.stderr)
            self.assertFalse((Path(directory) / 'build').exists())
