#!/usr/bin/env python3
"""Run reviewed portable cmd tests once; unclassified tests stay cross-platform."""
import argparse
import hashlib
import json
import os
import sys
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]
MANIFEST = Path(__file__).with_name('portable-cmd-tests.json')


def portable_tests(root=ROOT, manifest=MANIFEST):
    names = []
    for path, entry in json.loads(manifest.read_text()).items():
        source = (root / path).read_bytes().replace(b'\r\n', b'\n')
        if hashlib.sha256(source).hexdigest() != entry['sha256']:
            # Changed tests are unreviewed again, including additions to a
            # previously portable file. The platform group runs them normally.
            continue
        for name in entry['tests']:
            if not re.fullmatch(r'Test[A-Za-z0-9_]+', name) or name in names:
                raise ValueError(f'invalid or duplicate portable test: {name}')
            if not re.search(r'^func ' + re.escape(name) + r'\(t \*testing\.T\)', source.decode(), re.M):
                raise ValueError(f'portable test no longer declared: {name}')
            names.append(name)
    return sorted(names)


def test_command(group, names):
    command = ['go', 'test', '-count=1', './cmd']
    pattern = '^(' + '|'.join(names) + ')$' if names else '^$'
    if group == 'portable':
        if not names:
            raise ValueError('no reviewed portable tests; update the manifest or run full checks')
        command += ['-run', pattern]
    elif group == 'platform':
        command += ['-skip', pattern]
    return command


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('group', choices=('portable', 'platform', 'all'))
    parser.add_argument('--list', action='store_true')
    args = parser.parse_args()
    names = portable_tests()
    if args.list:
        print(json.dumps({'portable_tests': names, 'command': test_command(args.group, names)}, indent=2))
        return
    if os.environ.get('CI_PROFILE') == '1':
        subprocess.run([sys.executable, str(Path(__file__).with_name('profile_cmd.py')), args.group], check=True)
    else:
        subprocess.run(test_command(args.group, names), cwd=ROOT / 'cli', check=True)


if __name__ == '__main__':
    main()
