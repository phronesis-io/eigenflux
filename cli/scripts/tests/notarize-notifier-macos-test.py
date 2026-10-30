"""Exercise concurrent notarization and fail-closed publication without Apple access."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / 'notarize-notifier-macos.sh'


class NotarizationTest(unittest.TestCase):
    def test_parallel_jobs_and_failure(self):
        for failure in ('', 'amd64', 'arm64'):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                mock = root / 'mock'
                mock.write_text('''#!/bin/bash
set -eu
command="$1"; shift
if [[ "$command" == codesign && "$1" == -d ]]; then
  echo 'Authority=Developer ID Application: Test' >&2
elif [[ "$command" == xcrun && "$1" == notarytool ]]; then
  arch=arm64
  [[ "$3" != *amd64* ]] || arch=amd64
  touch "$TEST_ROOT/$arch.started"
  for ((i=0;i<100;i++)); do
    if [[ -e "$TEST_ROOT/amd64.started" && -e "$TEST_ROOT/arm64.started" ]]; then
      [[ "$FAIL_ARCH" != "$arch" ]]
      exit
    fi
    sleep 0.02
  done
  exit 9
elif [[ "$command" == xcrun && "$1" == stapler ]]; then
  echo "$*" >> "$TEST_ROOT/stapled"
fi
''')
                mock.chmod(0o755)
                source = SCRIPT.read_text()
                for tool in ('/usr/bin/codesign', '/usr/bin/ditto', '/usr/bin/xcrun', '/usr/sbin/spctl'):
                    source = source.replace(tool, f'"{mock}" {Path(tool).name}')
                source = source.replace('$(uname -s)', 'Darwin')
                script = root / 'test.sh'
                script.write_text(source)
                env = dict(os.environ, EIGENFLUX_NOTARY_PROFILE='test', TEST_ROOT=tmp, FAIL_ARCH=failure)
                result = subprocess.run(['bash', str(script), tmp], env=env, timeout=10)
                self.assertEqual(result.returncode, 1 if failure else 0)
                for arch in ('amd64', 'arm64'):
                    self.assertTrue((root / f'{arch}.started').exists())
                if not failure:
                    self.assertEqual(len((root / 'stapled').read_text().splitlines()), 4)


if __name__ == '__main__':
    unittest.main()
