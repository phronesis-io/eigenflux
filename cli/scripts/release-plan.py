#!/usr/bin/env python3
"""Read-only release decisions; uncertain CI evidence always requires tests."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
from urllib.request import Request, urlopen
import zipfile

ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = '.github/workflows/cli-auth-cross-platform.yml'
WORKFLOW_NAME = 'CLI Auth Cross-Platform'
SHA = re.compile(r'^[0-9a-f]{40}$')
REPOSITORY = re.compile(r'^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$')
VERSION = re.compile(r'^CLI_VERSION=((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))$', re.M)


def ci_evidence(data, repository, sha):
    runs = data.get('workflow_runs') if isinstance(data, dict) else None
    if not isinstance(runs, list) or not runs or not isinstance(runs[0], dict):
        return False, '', 'ci_missing'
    # GitHub lists newest runs first. A newer pending/failed run cannot fall
    # through to an older green run for the same commit.
    run = runs[0]
    path = run.get('path', '')
    repo = repository.lower()
    if (run.get('head_sha') != sha or run.get('event') != 'push'
            or run.get('head_branch') != 'main'
            or run.get('name') != WORKFLOW_NAME
            or path not in (WORKFLOW, WORKFLOW + '@refs/heads/main')
            or (run.get('head_repository') or {}).get('full_name', '').lower() != repo
            or (run.get('repository') or {}).get('full_name', repository).lower() != repo):
        return False, '', 'ci_identity_mismatch'
    if run.get('status') != 'completed' or run.get('conclusion') != 'success':
        return False, '', 'ci_not_successful'
    identifier = run.get('id')
    if not isinstance(identifier, int) or isinstance(identifier, bool) or identifier <= 0:
        return False, '', 'ci_identity_mismatch'
    return True, str(identifier), 'ci_verified'


def trusted_ci(repository, sha):
    if (not REPOSITORY.fullmatch(repository) or not SHA.fullmatch(sha)
            or any(part in ('.', '..') for part in repository.split('/'))):
        return False, '', 'ci_identity_mismatch'
    try:
        result = subprocess.run([
            'gh', 'api', f'repos/{repository}/actions/workflows/{Path(WORKFLOW).name}/runs',
            '--method', 'GET', '-f', f'head_sha={sha}', '-f', 'branch=main',
            '-f', 'event=push', '-f', 'per_page=1',
        ], check=True, capture_output=True, text=True, timeout=30)
        return ci_evidence(json.loads(result.stdout), repository, sha)
    except (OSError, subprocess.SubprocessError, ValueError, TypeError, AttributeError):
        return False, '', 'ci_unavailable'


def cli_version_at(sha):
    if not SHA.fullmatch(sha) or sha == '0' * 40:
        raise ValueError('missing commit')
    result = subprocess.run(['git', '-C', str(ROOT), 'show', f'{sha}:cli/.cli.config'],
                            check=True, capture_output=True, text=True, timeout=10)
    matches = VERSION.findall(result.stdout)
    if len(matches) != 1:
        raise ValueError('invalid CLI version')
    return matches[0]


def defer_skills(event_name, event, repository, sha):
    if event_name == 'workflow_run':
        try:
            run = event['workflow_run']
            if ((run.get('conclusion') != 'success' and not (event.get('action') == 'in_progress' and run.get('status') == 'in_progress'))
                    or run.get('head_branch') != 'main'
                    or run.get('name') != 'Release CLI'
                    or run.get('path', '.github/workflows/release-cli.yml') != '.github/workflows/release-cli.yml'
                    or (run.get('head_repository') or {}).get('full_name', '').lower() != repository.lower()):
                return True, 'upstream_identity_unknown'
            changed = cli_version_at(run['head_sha']) != cli_version_at(sha)
            return changed, 'newer_cli_pending' if changed else 'released_cli_matches'
        except (KeyError, ValueError, TypeError, AttributeError, OSError, subprocess.SubprocessError):
            return True, 'upstream_version_unknown'
    if event_name != 'push':
        return False, 'not_push'
    # An unknown previous release must not publish Skills ahead of a new CLI.
    try:
        if (event.get('ref') != 'refs/heads/main'
                or not SHA.fullmatch(event.get('after', '')) or not SHA.fullmatch(sha)
                or (event.get('repository') or {}).get('full_name', '').lower() != repository.lower()):
            return True, 'push_identity_unknown'
        if event['after'] != sha:
            # A backend-only commit may land after the Skills push was queued.
            # Accept the frozen latest main only when it contains that push.
            subprocess.run(['git', '-C', str(ROOT), 'merge-base', '--is-ancestor', event['after'], sha],
                           check=True, capture_output=True, text=True, timeout=10)
        changed = cli_version_at(event['before']) != cli_version_at(sha)
        return changed, 'cli_version_changed' if changed else 'cli_version_unchanged'
    except (KeyError, ValueError, TypeError, AttributeError, OSError, subprocess.SubprocessError):
        return True, 'push_version_unknown'


def publication_receipt(event_name, event, sha):
    """Scope reuse to one frozen source and upstream attempt, never a version."""
    run = event.get('workflow_run', {}) if event_name == 'workflow_run' else {}
    identifier, attempt = run.get('id', 0), run.get('run_attempt', 0)
    if (not SHA.fullmatch(sha) or any(type(v) is not int or v < 0 for v in (identifier, attempt))
            or (event_name == 'workflow_run' and (not identifier or not attempt))):
        raise ValueError('invalid publication identity')
    return f'skills-publication-{sha}-{identifier}-{attempt}'


def github_json(repository, endpoint, *query):
    result = subprocess.run(['gh', 'api', f'repos/{repository}/{endpoint}',
                             '--method', 'GET', *query], check=True,
                            capture_output=True, text=True, timeout=30)
    return json.loads(result.stdout)


def live_matches(manifest, base):
    """Recheck both consumer lanes and the canonical install entry before reuse."""
    expected = json.loads(manifest)
    digest, revision = expected['tar_sha256'], expected['revision']
    if not re.fullmatch(r'[0-9a-f]{64}', digest) or not re.fullmatch(r'[0-9a-f]{16}', revision):
        return False
    def read(path, limit):
        request = Request(base.rstrip('/') + '/' + path, headers={'Cache-Control': 'no-cache'})
        with urlopen(request, timeout=20) as response:
            return response.read(limit + 1), response.headers
    for lane in ('skills/latest', 'cli/latest'):
        data, _ = read(f'{lane}/manifest.json', len(manifest))
        if data != manifest:
            return False
        data, _ = read(f'{lane}/skills.tar.gz?rev={revision}', 32 * 1024 * 1024)
        if hashlib.sha256(data).hexdigest() != digest:
            return False
        data, _ = read(f'{lane}/skills.tar.gz.sha256', 256)
        if data.decode().strip() != digest:
            return False
    canonical = (ROOT / 'skills/install.md').read_bytes()
    data, headers = read('skills/latest/install.md', len(canonical))
    return (bool(canonical) and data == canonical
            and 'no-store' in {v.strip().lower() for v in headers.get('Cache-Control', '').split(',')})


def duplicate_publication(repository, event, receipt):
    # Manual force is an explicit repair/verification request. A failed or
    # uncertain previous run never suppresses tests or publication safeguards.
    if (event.get('inputs') or {}).get('force') in (True, 'true'):
        return False, '', 'forced_verification'
    if not REPOSITORY.fullmatch(repository) or any(p in ('.', '..') for p in repository.split('/')):
        return False, '', 'publication_identity_unknown'
    try:
        artifacts = github_json(repository, 'actions/artifacts', '-f', f'name={receipt}',
                                '-f', 'per_page=1')['artifacts']
        if not artifacts:
            return False, '', 'publication_missing'
        artifact = artifacts[0]
        identifier = artifact['workflow_run']['id']
        if (artifact['name'] != receipt or artifact['expired'] is not False
                or type(identifier) is not int or identifier <= 0
                or str(identifier) == os.environ.get('GITHUB_RUN_ID')):
            return False, '', 'publication_unusable'
        run = github_json(repository, f'actions/runs/{identifier}')
        if (run.get('id') != identifier
                or run.get('status') != 'completed' or run.get('conclusion') != 'success'
                or run.get('head_branch') != 'main' or run.get('name') != 'Release Skills'
                or run.get('path') != '.github/workflows/release-skills.yml'
                or (run.get('head_repository') or {}).get('full_name', '').lower() != repository.lower()):
            return False, '', 'publication_not_successful'
        artifact_id = artifact['id']
        if type(artifact_id) is not int or artifact_id <= 0:
            return False, '', 'publication_unusable'
        download = subprocess.run(['gh', 'api', f'repos/{repository}/actions/artifacts/{artifact_id}/zip'],
                                  check=True, capture_output=True, timeout=30)
        with zipfile.ZipFile(io.BytesIO(download.stdout)) as archive:
            entry = archive.getinfo('manifest.json')
            if entry.file_size > 1024 * 1024:
                return False, '', 'publication_unusable'
            manifest = archive.read(entry)
        if not live_matches(manifest, os.environ.get('R2_PUBLIC_URL') or 'https://cdn.eigenflux.ai'):
            return False, '', 'publication_live_changed'
        return True, str(identifier), 'publication_verified'
    except (OSError, subprocess.SubprocessError, ValueError, KeyError, TypeError,
            AttributeError, RuntimeError, zipfile.BadZipFile):
        return False, '', 'publication_unavailable'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repository', default=os.environ.get('GITHUB_REPOSITORY', ''))
    parser.add_argument('--sha', default=os.environ.get('GITHUB_SHA', ''))
    parser.add_argument('--event-name', default=os.environ.get('GITHUB_EVENT_NAME', ''))
    parser.add_argument('--event-path', type=Path, default=os.environ.get('GITHUB_EVENT_PATH'))
    parser.add_argument('--github-output', type=Path, default=os.environ.get('GITHUB_OUTPUT'))
    parser.add_argument('--check-duplicate', action='store_true')
    args = parser.parse_args()
    evidence, identifier, ci_reason = trusted_ci(args.repository, args.sha)
    event = {}
    if args.event_name in ('push', 'workflow_run', 'workflow_dispatch'):
        try:
            event = json.loads(args.event_path.read_text(encoding='utf-8'))
        except (OSError, ValueError, AttributeError):
            pass
    deferred, defer_reason = defer_skills(args.event_name, event, args.repository, args.sha)
    plan = {'trusted_ci': evidence, 'ci_run_id': identifier, 'ci_reason': ci_reason,
            'defer_to_cli': deferred, 'defer_reason': defer_reason}
    if args.check_duplicate:
        receipt = '' if deferred else publication_receipt(args.event_name, event, args.sha)
        duplicate, previous, reason = (False, '', 'publication_deferred') if deferred else duplicate_publication(
            args.repository, event, receipt)
        plan.update(publication_receipt=receipt, duplicate_release=duplicate,
                    duplicate_run_id=previous, duplicate_reason=reason)
    print(json.dumps(plan, sort_keys=True))
    if args.github_output:
        with args.github_output.open('a', encoding='utf-8') as output:
            for name, value in plan.items():
                output.write(f'{name}={str(value).lower() if isinstance(value, bool) else value}\n')


if __name__ == '__main__':
    main()
