"""Exercise release preflight, upload failure and public verification in isolation."""
import base64
import json
import functools
import hashlib
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import unittest
from urllib.error import HTTPError

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('artifacts', ROOT / 'cli/scripts/cli-artifacts.py')
artifacts = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(artifacts)


class QuietHandler(SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass


class ArtifactTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='cli release ')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.build = self.root / 'build/cli'
        self.build.mkdir(parents=True)
        for name in artifacts.BINARIES:
            (self.build / name).write_bytes(('binary ' + name).encode())
        (self.build / 'version.txt').write_text('99.0.0\n')
        artifacts.generate(self.build)
        self.write_release()

    def write_release(self):
        manifest = {'version': '99.0.0',
                    'signature': base64.b64encode(b'fixture-signature'.ljust(64, b'0')).decode(),
                    'artifacts': {name: {'sha256': value, 'size': (self.build / name).stat().st_size}
                                  for name, value in artifacts.verify(self.build).items()}}
        (self.build / 'release.json').write_text(json.dumps(manifest))

    def test_checksums_describe_exact_bytes_for_all_six_platforms(self):
        self.assertEqual(len(artifacts.verify(self.build)), 6)
        for name in artifacts.BINARIES:
            self.assertEqual((self.build / (name + '.sha256')).read_text(),
                             hashlib.sha256((self.build / name).read_bytes()).hexdigest() + '\n')

    def test_missing_empty_or_changed_binary_and_missing_checksum_fail(self):
        target = self.build / artifacts.BINARIES[-1]
        original = target.read_bytes()
        for content in (None, b'', b'changed after checksumming'):
            with self.subTest(content=content):
                if content is None:
                    target.unlink()
                else:
                    target.write_bytes(content)
                with self.assertRaises((OSError, ValueError)):
                    artifacts.verify(self.build)
                target.write_bytes(original)
        target.with_name(target.name + '.sha256').unlink()
        with self.assertRaises(OSError):
            artifacts.verify(self.build)

    def test_generate_does_not_write_checksums_for_partial_build(self):
        for checksum in self.build.glob('*.sha256'):
            checksum.unlink()
        (self.build / artifacts.BINARIES[-1]).unlink()
        with self.assertRaises(ValueError):
            artifacts.generate(self.build)
        self.assertEqual(list(self.build.glob('*.sha256')), [])

    def start_server(self, directory):
        server = ThreadingHTTPServer(('127.0.0.1', 0),
                                     functools.partial(QuietHandler, directory=str(directory)))
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(thread.join)
        self.addCleanup(server.shutdown)
        return f'http://127.0.0.1:{server.server_port}'

    def test_public_verification_rejects_missing_checksum_changed_and_stale_payloads(self):
        remote = self.root / 'remote'
        shutil.copytree(self.build, remote)
        url = self.start_server(remote)
        artifacts.verify_public(self.build, url)
        target = remote / artifacts.BINARIES[0]
        checksum = remote / (target.name + '.sha256')
        checksum.unlink()
        try:
            artifacts.verify_public(self.build, url)
            self.fail("missing public checksum was accepted")
        except HTTPError as error:
            self.assertEqual(error.code, 404)
            error.close()
        shutil.copy(self.build / checksum.name, checksum)
        target.write_bytes(b'corrupt binary')
        with self.assertRaisesRegex(ValueError, 'published binary'):
            artifacts.verify_public(self.build, url)
        checksum.write_text(hashlib.sha256(target.read_bytes()).hexdigest())
        with self.assertRaisesRegex(ValueError, 'published checksum'):
            artifacts.verify_public(self.build, url)

    def publisher(self, fail_upload='', corrupt_public=False, race_mode='', get_failure=False):
        scripts = self.root / 'cli/scripts'
        scripts.mkdir(parents=True, exist_ok=True)
        for name in ('publish.sh', 'cli-artifacts.py'):
            shutil.copy(ROOT / 'cli/scripts' / name, scripts / name)
        (self.root / 'cli/.cli.config').write_text('CLI_VERSION=99.0.0\n')
        bindir = self.root / 'bin'
        bindir.mkdir(exist_ok=True)
        # Stub only the external object-store client; run the real publisher and
        # public verifier against an HTTP server serving the uploaded files.
        aws = bindir / 'aws'
        aws.write_text('''#!/usr/bin/env python3
import os, pathlib, shutil, sys
args = sys.argv[1:]
public = pathlib.Path(os.environ['PUBLIC_ROOT'])
if args[:2] == ['s3api', 'get-object']:
    key = args[args.index('--key') + 1]
    path = public / key
    dest = args[args.index('--key') + 2]
    if os.environ.get('GET_FAILURE') == '1':
        print('An error occurred (AccessDenied)', file=sys.stderr)
        sys.exit(1)
    if not path.exists():
        print('An error occurred (NoSuchKey) when calling the GetObject operation: missing', file=sys.stderr)
        sys.exit(1)
    shutil.copyfile(path, dest)
    sys.exit(0)
if args[:2] == ['s3api', 'put-object']:
    key = args[args.index('--key') + 1]
    assert args[args.index('--if-none-match') + 1] == '*'
    source = args[args.index('--body') + 1]
    target = 's3://bucket/' + key
else:
    assert args[:2] == ['s3', 'cp']
    source, target = args[2:4]
with open(os.environ['UPLOAD_LOG'], 'a') as log:
    log.write(target + '\\n')
if os.environ.get('FAIL_UPLOAD') == target:
    sys.exit(1)
path = public / target.removeprefix('s3://bucket/')
path.parent.mkdir(parents=True, exist_ok=True)
if args[:2] == ['s3api', 'put-object']:
    if os.environ.get('RACE_MODE'):
        shutil.copyfile(source, path)
        if os.environ['RACE_MODE'] == 'different':
            path.write_bytes(b'conflicting concurrent release')
        sys.exit(1)
    if path.exists():
        sys.exit(1)
shutil.copyfile(source, path)
if os.environ.get('CORRUPT_PUBLIC') == '1' and path.name.endswith('.sha256'):
    path.write_text('0' * 64)
''')
        aws.chmod(0o755)
        public = self.root / 'public'
        public.mkdir(exist_ok=True)
        log = self.root / 'uploads'
        env = dict(os.environ, PATH=str(bindir) + os.pathsep + os.environ['PATH'],
                   R2_ACCESS_KEY_ID='test', R2_SECRET_ACCESS_KEY='test',
                   R2_ENDPOINT='http://unused.invalid', R2_BUCKET='bucket',
                   R2_PUBLIC_URL=self.start_server(public), UPLOAD_LOG=str(log),
                   PUBLIC_ROOT=str(public), FAIL_UPLOAD=fail_upload,
                   CORRUPT_PUBLIC='1' if corrupt_public else '0',
                   RACE_MODE=race_mode, GET_FAILURE='1' if get_failure else '0',
                   EIGENFLUX_PUBLISH_SKILLS_WITH_CLI='false')
        result = subprocess.run(['bash', str(scripts / 'publish.sh')], env=env,
                                capture_output=True, text=True, timeout=30)
        return result, log.read_text().splitlines() if log.exists() else []

    def test_publish_uploads_each_binary_and_checksum_before_version_pointer(self):
        result, calls = self.publisher()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(calls), 28)
        self.assertEqual(calls[-2:], ['s3://bucket/cli/latest/version.txt',
                                    's3://bucket/cli/latest/release.json'])
        for name in artifacts.BINARIES:
            for channel in ('99.0.0', 'latest'):
                for suffix in ('', '.sha256'):
                    self.assertIn(f's3://bucket/cli/{channel}/{name}{suffix}', calls)

    def test_preflight_rejects_incomplete_build_before_any_upload(self):
        (self.build / (artifacts.BINARIES[-1] + '.sha256')).unlink()
        result, calls = self.publisher()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, [])

    def test_preflight_rejects_wrong_version_before_any_upload(self):
        (self.build / 'version.txt').write_text('98.0.0')
        result, calls = self.publisher()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, [])

    def test_failed_upload_stops_without_advertising_release(self):
        for channel in ('99.0.0', 'latest'):
            with self.subTest(channel=channel):
                (self.root / 'uploads').unlink(missing_ok=True)
                failed = f's3://bucket/cli/{channel}/{artifacts.BINARIES[0]}'
                result, calls = self.publisher(fail_upload=failed)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(calls[-1], failed)
                self.assertNotIn('s3://bucket/cli/latest/version.txt', calls)
                self.assertNotIn('s3://bucket/cli/latest/release.json', calls)

    def test_public_corruption_stops_without_advertising_release(self):
        result, calls = self.publisher(corrupt_public=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('s3://bucket/cli/latest/version.txt', calls)
        self.assertNotIn('s3://bucket/cli/latest/release.json', calls)

    def test_release_preflight_rejects_unsigned_mismatched_and_partial_manifest(self):
        path = self.build / 'release.json'
        original = json.loads(path.read_text())
        for kind in ('missing', 'unsigned', 'wrong_version', 'wrong_size', 'wrong_hash', 'missing_platform'):
            with self.subTest(kind=kind):
                manifest = json.loads(json.dumps(original))
                if kind == 'missing':
                    path.unlink()
                else:
                    if kind == 'unsigned':
                        manifest.pop('signature')
                    elif kind == 'wrong_version':
                        manifest['version'] = '98.0.0'
                    elif kind == 'wrong_size':
                        manifest['artifacts'][artifacts.BINARIES[0]]['size'] += 1
                    elif kind == 'wrong_hash':
                        manifest['artifacts'][artifacts.BINARIES[0]]['sha256'] = '0' * 64
                    elif kind == 'missing_platform':
                        manifest['artifacts'].pop(artifacts.BINARIES[-1])
                    path.write_text(json.dumps(manifest))
                result, calls = self.publisher()
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(calls, [])
                path.write_text(json.dumps(original))

    def test_identical_release_retry_does_not_rewrite_immutable_version(self):
        result, calls = self.publisher()
        self.assertEqual(result.returncode, 0, result.stderr)
        (self.root / 'uploads').unlink()
        result, calls = self.publisher()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 14)
        self.assertTrue(all('/cli/latest/' in path for path in calls))

    def test_conflicting_partial_release_stops_before_any_write(self):
        target = self.root / 'public/cli/99.0.0/release.json'
        target.parent.mkdir(parents=True)
        target.write_text('conflicting signed manifest')
        result, calls = self.publisher()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, [])

    def test_store_failure_is_not_treated_as_a_missing_object(self):
        result, calls = self.publisher(get_failure=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, [])

    def test_concurrent_immutable_creation_accepts_only_identical_bytes(self):
        for race in ('different', 'same'):
            with self.subTest(race=race):
                shutil.rmtree(self.root / 'public', ignore_errors=True)
                (self.root / 'uploads').unlink(missing_ok=True)
                result, calls = self.publisher(race_mode=race)
                if race == 'same':
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(calls[-1], 's3://bucket/cli/latest/release.json')
                else:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(any('/cli/latest/' in path for path in calls))

    def test_public_manifest_verification_rejects_stale_signed_release(self):
        public = self.root / 'manifest-public'
        shutil.copytree(self.build, public)
        url = self.start_server(public)
        artifacts.verify_release_public(self.build, url)
        (public / 'release.json').write_text('stale signed release')
        with self.assertRaisesRegex(ValueError, 'manifest differs'):
            artifacts.verify_release_public(self.build, url)


if __name__ == '__main__':
    unittest.main()
