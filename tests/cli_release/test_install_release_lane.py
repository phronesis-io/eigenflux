"""Exercise installer channel selection without installing software."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ShellReleaseLane(unittest.TestCase):
    def test_version_channel_fallback_is_404_only(self):
        source = (ROOT / 'static/install.sh').read_text()
        function = source[source.index('fetch_latest_cli_version() {'):source.index('\ninstall_cli() {')]
        for mode, expected, count in [('ok', '1.0.6', 1), ('absent', '1.0.5', 2),
                                      ('server_error', None, 1), ('network_error', None, 1),
                                      ('malformed', None, 1)]:
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                curl = root / 'curl'
                curl.write_text('''#!/bin/sh
for arg do url="$arg"; done
printf '%s\\n' "$url" >> "$REQUEST_LOG"
case "$url" in
 */cli/v2/latest/version.txt)
   case "$MODE" in
     ok) printf '1.0.6\\n200' ;;
     absent) printf 'missing\\n404' ;;
     server_error) printf 'unavailable\\n503' ;;
     network_error) exit 7 ;;
     malformed) printf '<html>\\n200' ;;
   esac ;;
 */cli/latest/version.txt) echo 1.0.5 ;;
 *) exit 1 ;;
esac
''')
                curl.chmod(0o755)
                log = root / 'requests'
                env = dict(os.environ, PATH=str(root)+os.pathsep+os.environ['PATH'],
                           MODE=mode, REQUEST_LOG=str(log))
                result = subprocess.run(['sh', '-c', 'set -e\nCDN_URL=https://cdn.invalid\n'+function+'\nfetch_latest_cli_version'],
                                        env=env, capture_output=True, text=True)
                self.assertEqual(len(log.read_text().splitlines()), count)
                if expected:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout.strip(), expected)
                else:
                    self.assertNotEqual(result.returncode, 0)
