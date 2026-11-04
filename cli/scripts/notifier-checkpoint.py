#!/usr/bin/env python3
"""Durable signed-App and Apple submission checkpoints, separate from CLI builds."""
import argparse
import importlib.util
import concurrent.futures
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import uuid

SCRIPT = Path(__file__).resolve().parent
NETWORK = ('-1005', '-1001', '-1003', '-1004', '-1006', '-1009', 'connection was lost', 'Could not connect')
_spec = importlib.util.spec_from_file_location('notifier_release', SCRIPT / 'notifier-release.py')
_store = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_store)


def run(args, retry=False):
    for attempt in range(5 if retry else 1):
        p = subprocess.run([str(x) for x in args], capture_output=True, text=True)
        if p.returncode == 0:
            return p.stdout
        error = p.stdout + p.stderr
        if not retry or not any(x in error for x in NETWORK) or attempt == 4:
            raise RuntimeError(error)
        time.sleep(min(5 * 2**attempt, 40))


def aws(*args):
    return subprocess.run(['aws', '--endpoint-url', os.environ['R2_ENDPOINT'],
                           's3api', *map(str, args), '--bucket', os.environ['R2_BUCKET']],
                          capture_output=True, text=True)


def download(key, path):
    p = aws('get-object', '--key', key, path)
    if p.returncode == 0:
        return True
    if 'An error occurred (NoSuchKey) when calling the GetObject operation' in p.stderr:
        return False
    raise RuntimeError(p.stderr)


def put_new(key, path):
    p = aws('put-object', '--key', key, '--body', path, '--if-none-match', '*')
    if p.returncode:
        # Only an exact interrupted/retried write may be reused.
        with tempfile.TemporaryDirectory() as tmp:
            previous = Path(tmp) / 'previous'
            if not download(key, previous) or previous.read_bytes() != path.read_bytes():
                raise RuntimeError('checkpoint collision or upload failed: ' + p.stderr)


def validate_app(app, version, team):
    # Checkpoint archives have no ready receipt yet. Pin their native signature,
    # bundle identity and architecture before they can reach the notary service.
    _store.validate_identity(app, version, team)


def submission_id(path, signed):
    if path.stat().st_size > 16384:
        raise RuntimeError('oversized Apple submission checkpoint')
    record = json.loads(path.read_text())
    if set(record) != {'id', 'signed_sha256'} or record['signed_sha256'] != _store.digest(signed):
        raise RuntimeError('Apple submission checkpoint does not match signed archive')
    identifier = record['id']
    if not isinstance(identifier, str):
        raise RuntimeError('invalid stored Apple submission identifier')
    try:
        if str(uuid.UUID(identifier)) != identifier.lower():
            raise ValueError('noncanonical UUID')
    except ValueError as error:
        raise RuntimeError('invalid stored Apple submission identifier') from error
    return identifier


def prepare(args, arch):
    directory = args.helpers_dir / ('darwin-' + arch)
    work = args.work_dir / arch
    work.mkdir(parents=True, exist_ok=True)
    signed = work / 'signed.zip'
    prefix = f'notifier-staging/{args.version}/{args.source}/{arch}'
    app = directory / 'EigenFlux Notifications.app'
    if download(prefix + '/signed.zip', signed):
        _store.inspect_archive(signed)
        # ditto preserves signed bundle metadata; directory is dedicated to this checkpoint.
        directory.mkdir(parents=True, exist_ok=True)
        run(['/usr/bin/ditto', '-x', '-k', signed, directory])
        validate_app(app, args.version, args.team_id)
    else:
        run(['/usr/bin/env', 'EIGENFLUX_NOTIFIER_VERSION=' + args.version,
             'bash', SCRIPT / 'build-notifier-macos.sh', directory, arch])
        validate_app(app, args.version, args.team_id)
        run(['/usr/bin/ditto', '-c', '-k', '--keepParent', app, signed])
        put_new(prefix + '/signed.zip', signed)
    return app, signed, prefix, work


def notarize(args, arch):
    app, signed, prefix, work = prepare(args, arch)
    submission = work / 'submission.json'
    profile = os.environ['EIGENFLUX_NOTARY_PROFILE']
    remote_submission = download(prefix + '/submission.json', submission)
    if remote_submission or submission.exists():
        # A local submission response survives a failed checkpoint upload. Never
        # submit the same signed archive again merely because that upload failed.
        identifier = submission_id(submission, signed)
        if not remote_submission:
            put_new(prefix + '/submission.json', submission)
    else:
        receipt = json.loads(run(['/usr/bin/xcrun', 'notarytool', 'submit', signed,
                                 '--keychain-profile', profile, '--output-format', 'json']))
        identifier = receipt['id']
        temporary = work / 'submission.tmp.json'
        temporary.write_text(json.dumps({'id': identifier, 'signed_sha256': _store.digest(signed)}, sort_keys=True))
        submission_id(temporary, signed)
        temporary.replace(submission)
        put_new(prefix + '/submission.json', submission)
    print(f'{arch}: resume Apple submission {identifier}', flush=True)
    receipt = json.loads(run(['/usr/bin/xcrun', 'notarytool', 'wait', identifier,
                             '--keychain-profile', profile, '--output-format', 'json', '--timeout', '20m'], retry=True))
    if receipt.get('status') != 'Accepted':
        raise RuntimeError('Apple did not accept notification App: ' + str(receipt.get('status')))
    (work / 'accepted.json').write_text(json.dumps(receipt, sort_keys=True))
    run(['/usr/bin/xcrun', 'stapler', 'staple', app], retry=True)
    # Full ticket/Gatekeeper validation belongs to formal publication, once.


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--version', required=True)
    p.add_argument('--source', required=True)
    p.add_argument('--team-id', required=True)
    p.add_argument('--helpers-dir', required=True, type=Path)
    p.add_argument('--work-dir', required=True, type=Path)
    args = p.parse_args()
    if not re.fullmatch(r'\d+\.\d+\.\d+', args.version) or not re.fullmatch(r'[a-f0-9]{64}', args.source) or not re.fullmatch(r'[A-Z0-9]{10}', args.team_id):
        p.error('invalid checkpoint identity')
    os.environ['AWS_ACCESS_KEY_ID'] = os.environ['R2_ACCESS_KEY_ID']
    os.environ['AWS_SECRET_ACCESS_KEY'] = os.environ['R2_SECRET_ACCESS_KEY']
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:
        list(executor.map(lambda arch: notarize(args, arch), ('amd64', 'arm64')))


if __name__ == '__main__':
    main()
