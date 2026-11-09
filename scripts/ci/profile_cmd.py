"""Temporary read-only timing instrumentation; runs the unchanged test selection."""
import json
from pathlib import Path
import subprocess
import sys
import time
import cmd_tests

root = Path(__file__).resolve().parents[2]
out = root / 'build/ci-profile'
out.mkdir(parents=True, exist_ok=True)
command = cmd_tests.test_command(sys.argv[1], cmd_tests.portable_tests())
command.insert(2, '-json')
start = time.monotonic()
with (out / 'cmd-tests.jsonl').open('w') as log:
    result = subprocess.run(command, cwd=root / 'cli', stdout=log, stderr=subprocess.STDOUT)
seconds = time.monotonic() - start
rows = []
for line in (out / 'cmd-tests.jsonl').read_text(encoding='utf-8', errors='replace').splitlines():
    try:
        row = json.loads(line)
    except ValueError:
        continue
    if row.get('Action') in ('pass', 'fail') and row.get('Test'):
        rows.append(row)
summary = {'wall_seconds': seconds, 'exit_code': result.returncode,
           'top_level_tests': sorted([r for r in rows if '/' not in r['Test']], key=lambda r: -r['Elapsed'])[:30],
           'subtests': sorted([r for r in rows if '/' in r['Test']], key=lambda r: -r['Elapsed'])[:30]}
(out / 'summary.json').write_text(json.dumps(summary, indent=2), encoding='utf-8')
print(json.dumps(summary, indent=2))
sys.exit(result.returncode)
