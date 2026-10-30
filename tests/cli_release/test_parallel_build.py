"""Exercise the production compiler orchestration without native toolchains."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


@unittest.skipUnless(shutil.which('bash'), 'Bash required')
class ParallelBuildTests(unittest.TestCase):
    def run_build(self, failing):
        source = (ROOT / 'cli/scripts/build.sh').read_text()
        section = source[source.index('compile_platform() {'):source.index('# Write version file')]
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory)
            worker = stage / 'worker.py'
            worker.write_text('''import os,time,pathlib,sys
p=pathlib.Path(os.environ['BUILD_DIR'])
n=os.environ['GOOS']+'-'+os.environ['GOARCH']
(p/(n+'.start')).write_text(str(time.monotonic_ns()))
time.sleep(0.15)
(p/(n+'.end')).write_text(str(time.monotonic_ns()))
sys.exit(1 if n==os.environ.get('FAIL_PLATFORM') else 0)
''')
            import sys
            script = '''set -e
failed=0
PLATFORMS=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)
GO_CMD=("$PYTHON" "$WORKER")
''' + section
            env = dict(os.environ, BUILD_DIR=directory, PYTHON=sys.executable,
                       WORKER=str(worker), FAIL_PLATFORM=failing)
            result = subprocess.run(['bash', '-c', script], env=env, capture_output=True, text=True)
            starts = list(stage.glob('*.start'))
            ends = list(stage.glob('*.end'))
            self.assertEqual(len(starts), 6)
            self.assertEqual(len(ends), 6, 'all workers must finish before return')
            events = [(int(p.read_text()), 1) for p in starts] + [(int(p.read_text()), -1) for p in ends]
            active = peak = 0
            for _, delta in sorted(events):
                active += delta
                peak = max(peak, active)
            self.assertGreater(peak, 1)
            self.assertLessEqual(peak, 3)
            self.assertEqual(result.returncode, 1 if failing else 0, result.stderr)
            if failing:
                self.assertIn('packaging stopped', result.stderr)
                self.assertIn('Compilation FAILED: linux/amd64', result.stderr)

    def test_success(self):
        self.run_build('')

    def test_failed_worker_is_not_hidden(self):
        self.run_build('linux-amd64')
