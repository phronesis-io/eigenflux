#!/usr/bin/env python3
"""Install a local, test-only preview without changing the user's live setup."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys


def run(argv):
    env = dict(os.environ)
    env.pop('EIGENFLUX_SKILLS_DIR', None)
    return subprocess.run([str(a) for a in argv], check=True, text=True,
                          stdout=subprocess.PIPE, env=env).stdout.strip()


def install(package, root, binary):
    source = json.loads((package / 'preview/source.json').read_text())
    if not root.is_absolute() or root.is_symlink():
        raise ValueError('Use an absolute, non-symlink preview root.')
    for parent in root.parents:
        if parent.is_symlink():
            raise ValueError('Preview root ancestors must not be symlinks.')
    receipt_path = root / 'preview-receipt.json'
    previous = None
    if root.exists():
        if not receipt_path.is_file():
            raise ValueError('Root already exists without a preview receipt; choose a new root.')
        previous = json.loads(receipt_path.read_text())
        if (previous.get('kind') != source['kind'] or
                previous.get('source_commit') != source['source_commit'] or
                previous.get('root') != str(root)):
            raise ValueError('Root belongs to a different preview; do not overwrite it.')
        if any(p.is_symlink() for p in root.rglob('*')):
            raise ValueError('Preview root contains a symlink; refusing to overwrite it.')
    if not binary.is_file():
        raise ValueError('CLI binary missing. Use the macOS bundle or build the pinned source first.')
    binary = binary.resolve()
    version = run([binary, 'version', '--short'])
    if version != source['cli_version']:
        raise ValueError('CLI version must match source.json; use the packaged build.')
    actual_commit = json.loads(run([binary, 'version', '--format', 'json'])).get('commit')
    if actual_commit != source['source_commit']:
        raise ValueError('CLI commit does not match the pinned source.')
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    if previous and previous.get('cli_sha256') != digest:
        raise ValueError('CLI changed; use a new preview root.')

    root.mkdir(parents=True, exist_ok=True)
    cli = root / 'bin/eigenflux'
    cli.parent.mkdir(exist_ok=True)
    if binary != cli:
        shutil.copy2(binary, cli)
    cli.chmod(0o700)
    home = root / '.eigenflux'
    target = root / 'skills'
    receipt = {**source, 'root': str(root), 'agent_home': str(home),
               'skills_dir': str(target), 'cli': str(cli), 'cli_sha256': digest,
               'server': 'eigenflux', 'endpoint': 'https://www.eigenflux.ai',
               'runtime_mode': 'skill',
               'cli_argv': [str(cli), '--homedir', str(home), '--server', 'eigenflux', '--runtime-mode', 'skill'],
               'status': 'preparing'}
    receipt_path.write_text(json.dumps(receipt, indent=2) + '\n')
    prefix = [cli, '--homedir', home]
    # These existing commands only touch the selected local Home/Skills target.
    run(prefix + ['config', 'set', '--key', 'auto_skill_sync', '--value', 'false'])
    run(prefix + ['skills', 'install', '--from-bundle', package / 'skills', '--into', target])
    run(prefix + ['skills', 'target', 'set', '--path', target, '--host', 'terminal'])
    if run(prefix + ['skills', 'path', '--host', 'terminal']) != str(target):
        raise ValueError('Registered Skills target does not resolve to the preview.')
    listed = json.loads(run(prefix + ['skills', 'list', '--into', target, '--format', 'json']))
    if len(listed.get('skills', [])) < 4 or not all(x.get('sha_match') for x in listed['skills']):
        raise ValueError('Installed Skills verification failed.')
    for path in (package / 'skills').glob('ef-*/**/*'):
        if path.is_file():
            installed = target / path.relative_to(package / 'skills')
            if not installed.is_file() or installed.read_bytes() != path.read_bytes():
                raise ValueError('Installed Skill differs from package: ' + str(path))
    receipt['status'] = 'ready'
    receipt_path.write_text(json.dumps(receipt, indent=2) + '\n')
    guide = f'''# EigenFlux local onboarding preview

Test-only installation receipt: `{receipt_path}`. Read it before acting and after
restart. This is the supported bare-CLI/native-scheduler path; it does not test
plugin installation or a website popup. Do not run the public installer or
Skills sync; those would replace this pinned candidate.

Use `{cli}` for this test and preserve the receipt's Home/server on every command.
Use runtime mode `skill` for this native-scheduler preview. If this process has
an EIGENFLUX_SKILLS_DIR override, set it to `{target}` for test commands only;
verify `skills path` matches the receipt before continuing. Do not edit a global
shell profile or another Agent's environment.
When a Skill rule or returned plan spells the executable `eigenflux`, replace
only that executable token with this absolute binary path; preserve all following
tokens. Use the same executable in the narrowly scoped execution rule and native
scheduler launcher. Do not create a broader shell/interpreter allow rule.
This executable mapping is test scaffolding, not a production instruction.

Explicitly load `{target}/ef-onboarding/SKILL.md` and its selected references.
Load other ef-* references from `{target}`, not a globally installed copy.
Components are verified; begin at the normal welcome and first permission
question. Do not skip, simulate or pre-answer scheduling, execution or Prefill
choices. Native host approvals remain necessary. Do not copy production keys,
account state or scheduler entries into this Home. If the host cannot load these
files or has a conflicting automatic plugin, report that limitation before
testing; do not silently use another version or disable global integrations.

The endpoint is the real EigenFlux service. A new Home is a separate local
identity, not a sandbox server. To test new-user setup use a test email; reusing
an existing account's email can enter account recovery instead. Never report a
fake website completion to skip the real access gate.

After testing, remove only the test scheduler and exact test execution rule
created with consent, using their recorded IDs/paths. Then this dedicated root
can be removed. Removing files does not undo anything published to the network.
Do not touch unrelated tasks, Rules, accounts or globally installed Skills.
'''
    (root / 'START-HERE.md').write_text(guide)
    print(json.dumps(receipt, indent=2))
    print('\nOpen a new Agent task and ask it to read: ' + str(root / 'START-HERE.md'))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    package = Path(__file__).resolve().parents[1]
    source = json.loads((package / 'preview/source.json').read_text())
    parser.add_argument('--root', type=Path,
                        default=Path.home() / '.eigenflux-onboarding-preview' / source['source_commit'][:12])
    parser.add_argument('--cli', type=Path, default=package / 'bin/eigenflux')
    args = parser.parse_args()
    try:
        install(package, args.root.expanduser(), args.cli.expanduser())
    except (ValueError, OSError, subprocess.CalledProcessError) as exc:
        print('Preview installation stopped: ' + str(exc), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
