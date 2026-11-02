#!/usr/bin/env python3
"""Immutable native notification bundles; only absent ready receipts are cache misses."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import zipfile

CLI = Path(__file__).resolve().parents[1]
APP = 'EigenFlux Notifications.app'
ARCHES = ('amd64', 'arm64')
MAX_ARCHIVE = 64 << 20


def command(args, **kwargs):
    return subprocess.run(args, check=True, capture_output=True, text=True, **kwargs)


def aws_args(*args):
    return ['aws', '--endpoint-url', os.environ['R2_ENDPOINT'], '--region', 'auto',
            's3api', *args, '--bucket', os.environ['R2_BUCKET']]


def download(key, path, *, allow_missing=False):
    result = subprocess.run(aws_args('get-object', '--key', key, str(path)), capture_output=True, text=True)
    if result.returncode == 0:
        return True
    if allow_missing and 'An error occurred (NoSuchKey) when calling the GetObject operation' in result.stderr:
        return False
    raise RuntimeError('notifier storage read failed: ' + result.stderr.strip())


def immutable_upload(key, path):
    result = subprocess.run(aws_args('put-object', '--key', key, '--body', str(path),
                                   '--if-none-match', '*'), capture_output=True, text=True)
    if result.returncode == 0:
        return
    if 'PreconditionFailed' not in result.stderr and 'ConditionalRequestConflict' not in result.stderr:
        raise RuntimeError('notifier storage write failed: ' + result.stderr.strip())
    with tempfile.TemporaryDirectory() as directory:
        existing = Path(directory) / 'object'
        download(key, existing)
        if existing.read_bytes() != path.read_bytes():
            raise RuntimeError('immutable notifier object differs: ' + key)


def receipt_tool(mode, path):
    args = [os.environ['EIGENFLUX_NOTIFIER_RECEIPT_TOOL']] if os.environ.get('EIGENFLUX_NOTIFIER_RECEIPT_TOOL') else ['go', 'run', './cmd/notifierrelease']
    args += ['--mode', mode, '--receipt', str(path.resolve())]
    if mode == 'sign':
        args += ['--key-file', str(Path(os.environ['EIGENFLUX_SKILLS_SIGNING_KEY_FILE']).resolve())]
    return command(args, cwd=CLI).stdout


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def retry_online(args):
    for attempt in range(4):
        try:
            return command(args)
        except subprocess.CalledProcessError as error:
            diagnostic = (error.stderr or '') + (error.stdout or '')
            transient = re.search(r'NSURLErrorDomain Code=-(?:1001|1003|1004|1005|1006|1009)\b', diagnostic)
            if not transient or attempt == 3:
                raise RuntimeError('Apple notification verification failed: ' + diagnostic[-4096:]) from error
            time.sleep((5, 15, 30)[attempt])


def validate_identity(app, version, team, *, online=False):
    command(['/usr/bin/codesign', '--verify', '--deep', '--strict', str(app)])
    info = command(['/usr/bin/codesign', '-d', '--verbose=4', str(app)])
    evidence = info.stdout + info.stderr
    if not re.search(r'^Authority=Developer ID Application:', evidence, re.M) or not re.search(r'^TeamIdentifier=' + re.escape(team) + r'$', evidence, re.M):
        raise RuntimeError('unexpected notification signing identity')
    for key in ('CFBundleVersion', 'CFBundleShortVersionString'):
        value = command(['/usr/libexec/PlistBuddy', '-c', 'Print :' + key, str(app / 'Contents/Info.plist')]).stdout.strip()
        if value != version:
            raise RuntimeError('notification bundle version mismatch')
    bundle_id = command(['/usr/libexec/PlistBuddy', '-c', 'Print :CFBundleIdentifier', str(app / 'Contents/Info.plist')]).stdout.strip()
    if bundle_id != 'ai.eigenflux.notifications':
        raise RuntimeError('unexpected notification bundle identifier')
    arch = app.parent.name.removeprefix('darwin-')
    if arch not in ARCHES:
        raise RuntimeError('notification architecture scope missing')
    command(['/usr/bin/lipo', '-verify_arch', 'x86_64' if arch == 'amd64' else 'arm64', str(app / 'Contents/MacOS/EigenFluxNotifications')])
    if online:
        retry_online(['/usr/bin/xcrun', 'stapler', 'validate', str(app)])
        retry_online(['/usr/sbin/spctl', '--assess', '--type', 'execute', str(app)])


def inspect_archive(path):
    if path.stat().st_size > MAX_ARCHIVE:
        raise RuntimeError('notifier archive too large')
    with zipfile.ZipFile(path) as archive:
        entries = archive.infolist()
        if len(entries) > 4096 or sum(e.file_size for e in entries) > 256 << 20:
            raise RuntimeError('notifier expansion exceeds bounds')
        names = set()
        for entry in entries:
            name = entry.filename.rstrip('/')
            parts = PurePosixPath(name).parts
            mode = entry.external_attr >> 16
            if not parts or '\\' in name or '\x00' in name or name.startswith('/') or any(p in ('', '.', '..') or ':' in p for p in name.split('/')):
                raise RuntimeError('unsafe notifier archive path')
            if parts[0] not in (APP, '__MACOSX') or (parts[0] == '__MACOSX' and len(parts) > 1 and parts[1] not in (APP, '._' + APP)):
                raise RuntimeError('unexpected notifier archive root')
            if stat.S_ISLNK(mode) or stat.S_IFMT(mode) not in (0, stat.S_IFREG, stat.S_IFDIR):
                raise RuntimeError('unsupported notifier archive entry')
            if name.casefold() in names:
                raise RuntimeError('duplicate notifier archive path')
            names.add(name.casefold())
        if APP + '/Contents/Info.plist' not in {e.filename for e in entries} or archive.testzip() is not None:
            raise RuntimeError('invalid notification archive')


def archive_contents(path):
    """Compare native payloads and metadata, never ZIP timestamps/compression."""
    inspect_archive(path)
    members = {}
    with zipfile.ZipFile(path) as archive:
        for entry in archive.infolist():
            checksum = hashlib.sha256()
            with archive.open(entry) as stream:
                for chunk in iter(lambda: stream.read(65536), b''):
                    checksum.update(chunk)
            members[entry.filename] = (
                entry.external_attr >> 16, entry.file_size, checksum.hexdigest(),
            )
    return members


def adopt_uploaded_archive(prefix, arch, path):
    # A previous runner may have uploaded one ZIP before publishing ready.json.
    # Native tickets (including AppleDouble metadata) participate in the exact
    # member comparison. Only ZIP container metadata may differ across retries.
    with tempfile.TemporaryDirectory(dir=path.parent) as directory:
        existing = Path(directory) / (arch + '.zip')
        if not download(prefix + '/' + arch + '.zip', existing, allow_missing=True):
            return
        if archive_contents(existing) != archive_contents(path):
            raise RuntimeError('partially uploaded notifier payload differs: ' + arch)
        existing.replace(path)


def checked_receipt(path, version, source, team):
    record = json.loads(receipt_tool('verify', path))
    if record['version'] != version or record['source'] != source or record['team_id'] != team:
        raise RuntimeError('notifier receipt identity mismatch')
    return record


def checked_artifact(prefix, arch, path, record):
    download(prefix + '/' + arch + '.zip', path)
    expected = record['artifacts'][arch]
    if path.stat().st_size != expected['size'] or digest(path) != expected['sha256']:
        raise RuntimeError('notifier artifact checksum mismatch')
    inspect_archive(path)


def restore(args):
    args.work_dir.mkdir(parents=True, exist_ok=True)
    ready = args.work_dir / 'ready.json'
    prefix = f'notifier/{args.version}/{args.source}'
    if not download(prefix + '/ready.json', ready, allow_missing=True):
        return 3
    record = checked_receipt(ready, args.version, args.source, args.team_id)
    with tempfile.TemporaryDirectory(dir=args.work_dir) as directory:
        stage = Path(directory)
        for arch in ARCHES:
            archive = stage / (arch + '.zip')
            checked_artifact(prefix, arch, archive, record)
            destination = stage / ('darwin-' + arch)
            destination.mkdir()
            # ditto restores AppleDouble metadata carrying native bundle tickets.
            command(['/usr/bin/ditto', '-x', '-k', str(archive), str(destination)])
            validate_identity(destination / APP, args.version, args.team_id)
        if args.helpers_dir.exists():
            raise RuntimeError('helpers destination already exists')
        args.helpers_dir.mkdir(parents=True)
        for arch in ARCHES:
            shutil.move(str(stage / ('darwin-' + arch)), args.helpers_dir / ('darwin-' + arch))
    print('Restored verified notification App ' + args.version)
    return 0


def publish(args):
    args.work_dir.mkdir(parents=True, exist_ok=True)
    prefix = f'notifier/{args.version}/{args.source}'
    ready = args.work_dir / 'ready.json'
    if download(prefix + '/ready.json', ready, allow_missing=True):
        record = checked_receipt(ready, args.version, args.source, args.team_id)
        for arch in ARCHES:
            checked_artifact(prefix, arch, args.work_dir / (arch + '.zip'), record)
        print('Existing immutable notification release retained')
        return 0
    # A signed local candidate is a completed validation checkpoint, even when
    # an interrupted upload has not yet published its final ready marker.
    if ready.exists():
        record = checked_receipt(ready, args.version, args.source, args.team_id)
        for arch in ARCHES:
            archive = args.work_dir / (arch + '.zip')
            expected = record['artifacts'][arch]
            if archive.stat().st_size != expected['size'] or digest(archive) != expected['sha256']:
                raise RuntimeError('local notifier checkpoint checksum mismatch')
            inspect_archive(archive)
    else:
        artifacts = {}
        for arch in ARCHES:
            app = args.helpers_dir / ('darwin-' + arch) / APP
            validate_identity(app, args.version, args.team_id, online=True)
            archive = args.work_dir / (arch + '.zip')
            command(['/usr/bin/ditto', '-c', '-k', '--keepParent', str(app), str(archive)])
            inspect_archive(archive)
            adopt_uploaded_archive(prefix, arch, archive)
            artifacts[arch] = {'sha256': digest(archive), 'size': archive.stat().st_size}
        candidate = args.work_dir / 'candidate.json'
        candidate.write_text(json.dumps({'schema': 1, 'version': args.version, 'source': args.source,
                                        'team_id': args.team_id, 'validation': 'codesign_stapler_gatekeeper', 'artifacts': artifacts}))
        signed = receipt_tool('sign', candidate)
        # Never leave an unsigned ready.json that could be mistaken for a receipt.
        candidate.write_text(signed)
        checked_receipt(candidate, args.version, args.source, args.team_id)
        candidate.replace(ready)
    for arch in ARCHES:
        immutable_upload(prefix + '/' + arch + '.zip', args.work_dir / (arch + '.zip'))
    immutable_upload(prefix + '/ready.json', ready)  # Last: incomplete releases are never ready.
    print('Published immutable notification App ' + args.version)
    return 0


def verify_packages(args):
    # Reuse the existing signed receipt; never infer notarization from codesign.
    ready = args.work_dir / 'ready.json'
    record = json.loads(receipt_tool('verify', ready))
    if record['version'] != args.version or record['team_id'] != args.team_id:
        raise RuntimeError('notifier publication proof identity mismatch')
    for arch in ARCHES:
        native = args.work_dir / (arch + '.zip')
        expected = record['artifacts'][arch]
        if native.stat().st_size != expected['size'] or digest(native) != expected['sha256']:
            raise RuntimeError('notifier publication proof archive mismatch')
        inspect_archive(native)
        with zipfile.ZipFile(native) as original:
            files = {e.filename: e for e in original.infolist()
                     if e.filename.startswith(APP + '/') and not e.is_dir()}
            # ditto may store AppleDouble inside the App, not only __MACOSX.
            # Restoring consumes these as metadata; CLI packages contain files.
            for name in tuple(files):
                if PurePosixPath(name).name.startswith('._'):
                    with original.open(files[name]) as stream:
                        if stream.read(4) == b'\x00\x05\x16\x07':
                            del files[name]
            with zipfile.ZipFile(args.build_dir / ('eigenflux-darwin-' + arch + '.zip')) as package:
                entries = [e for e in package.infolist() if e.filename.startswith(APP + '/')]
                if len(entries) != len(files) or {e.filename for e in entries} != set(files):
                    raise RuntimeError('packaged notifier differs from notarization proof')
                for entry in entries:
                    source = files[entry.filename]
                    mode = entry.external_attr >> 16
                    expected_mode = 0o755 if (source.external_attr >> 16) & 0o111 else 0o644
                    if (not stat.S_ISREG(mode) or stat.S_IMODE(mode) != expected_mode
                            or entry.file_size != source.file_size
                            or package.read(entry) != original.read(source)):
                        raise RuntimeError('packaged notifier differs from notarization proof')
    print('Verified offline notarization proof for both packaged notification Apps')
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=('restore', 'publish', 'verify-packages'))
    parser.add_argument('--version', required=True)
    parser.add_argument('--source')
    parser.add_argument('--team-id', required=True)
    parser.add_argument('--helpers-dir', type=Path)
    parser.add_argument('--build-dir', type=Path)
    parser.add_argument('--work-dir', required=True, type=Path)
    args = parser.parse_args()
    if not re.fullmatch(r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', args.version) or (args.mode != 'verify-packages' and not re.fullmatch(r'[0-9a-f]{64}', args.source or '')) or not re.fullmatch(r'[A-Z0-9]{10}', args.team_id):
        raise RuntimeError('invalid notifier release identity')
    if args.mode == 'verify-packages':
        if args.build_dir is None:
            parser.error('verify-packages requires --build-dir')
        return verify_packages(args)
    if args.helpers_dir is None:
        parser.error('restore and publish require --helpers-dir')
    os.environ['AWS_ACCESS_KEY_ID'] = os.environ['R2_ACCESS_KEY_ID']
    os.environ['AWS_SECRET_ACCESS_KEY'] = os.environ['R2_SECRET_ACCESS_KEY']
    return restore(args) if args.mode == 'restore' else publish(args)


if __name__ == '__main__':
    try:
        sys.exit(main())
    except Exception as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
