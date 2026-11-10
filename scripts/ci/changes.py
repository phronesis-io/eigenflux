#!/usr/bin/env python3
"""Classify a complete Git diff; unknown paths require full platform checks."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]


def classify(paths):
    full = False
    skills = False
    for path in paths:
        if path in ('README.md', 'AGENTS.md', 'skills/AGENTS.md') or (path.startswith('docs/') and path.endswith('.md')):
            continue
        if (path.startswith('skills/') and Path(path).suffix.lower() in ('.md', '.json', '.yaml', '.yml', '.txt', '.png', '.jpg', '.jpeg', '.svg', '.webp')) or path in ('static/feed_contract.md', 'static/feed_baseline_contract.md'):
            skills = True
        elif path.startswith('tests/onboarding_prefill/'):
            skills = True
        else:
            full = True
    return {'full': full, 'skills': skills}


def changed_paths(event, base, head):
    if event not in ('pull_request', 'push'):
        return None
    if not all(re.fullmatch(r'[a-f0-9]{40}', value or '') and value != '0' * 40 for value in (base, head)):
        return None
    # Include removed/renamed files and every page of large PRs. PR checks run
    # against the merge checkout; compare it with the base's common ancestor.
    revision = base + ('...' if event == 'pull_request' else '..') + head
    data = subprocess.check_output(['git', 'diff', '--name-only', '--no-renames', '-z', revision, '--'], cwd=ROOT)
    return [p.decode('utf-8') for p in data.split(b'\0') if p]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--event', default=os.environ.get('CI_EVENT', ''))
    parser.add_argument('--base', default=os.environ.get('CI_BASE_SHA', ''))
    parser.add_argument('--head', default=os.environ.get('CI_HEAD_SHA', ''))
    parser.add_argument('--github-output', type=Path, default=os.environ.get('GITHUB_OUTPUT'))
    args = parser.parse_args()
    try:
        paths = changed_paths(args.event, args.base, args.head)
    except (OSError, subprocess.CalledProcessError, UnicodeError):
        paths = None
    result = {'full': True, 'skills': True} if paths is None else classify(paths)
    print(json.dumps(result))
    if args.github_output:
        with args.github_output.open('a') as output:
            for key, value in result.items():
                output.write(f'{key}={str(value).lower()}\n')


if __name__ == '__main__':
    main()
