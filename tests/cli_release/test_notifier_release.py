import importlib.util
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import zipfile

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('notifier_release', ROOT / 'cli/scripts/notifier-release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class NotifierReleaseTests(unittest.TestCase):
    def archive(self, path, extra=None):
        with zipfile.ZipFile(path, 'w') as z:
            z.writestr(release.APP + '/Contents/Info.plist', '<plist/>')
            if extra:
                z.writestr(*extra)

    def test_archive_rejects_escape_duplicates_and_links(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / 'a.zip'
            self.archive(p)
            release.inspect_archive(p)
            for name in ('../escape', release.APP + '/Contents/../escape', release.APP + '/./Contents/Info.plist', release.APP + '//Contents/Info.plist', '/absolute', '__MACOSX/evil/._file', release.APP + '/contents/info.plist'):
                with self.subTest(name=name):
                    self.archive(p, (name, 'x'))
                    with self.assertRaises(RuntimeError):
                        release.inspect_archive(p)
            link = zipfile.ZipInfo(release.APP + '/Contents/link')
            link.create_system = 3
            link.external_attr = (stat.S_IFLNK | 0o777) << 16
            self.archive(p, (link, '/etc/passwd'))
            with self.assertRaises(RuntimeError):
                release.inspect_archive(p)

    def test_reused_identity_check_is_offline_and_pins_team_and_bundle(self):
        calls = []
        def cmd(args):
            calls.append(args)
            if args[0] == '/usr/libexec/PlistBuddy':
                value = 'ai.eigenflux.notifications' if args[2].endswith('CFBundleIdentifier') else '1.0.0'
                return subprocess.CompletedProcess(args, 0, value, '')
            return subprocess.CompletedProcess(args, 0, '', 'Authority=Developer ID Application: Example\nTeamIdentifier=ABCDEFGHIJ\n')
        app = Path('/fixture/darwin-arm64') / release.APP
        with patch.object(release, 'command', side_effect=cmd):
            release.validate_identity(app, '1.0.0', 'ABCDEFGHIJ')
            with self.assertRaisesRegex(RuntimeError, 'signing identity'):
                release.validate_identity(app, '1.0.0', 'OTHERTEAM1X')
        self.assertFalse(any('stapler' in a or 'spctl' in a[0] for a in calls))
        self.assertTrue(any(a[0] == '/usr/bin/lipo' for a in calls))

    def test_storage_only_missing_is_miss(self):
        env = {'R2_ENDPOINT': 'https://storage.test', 'R2_BUCKET': 'test'}
        with patch.dict(os.environ, env), patch.object(release.subprocess, 'run') as run:
            for error, missing in [('An error occurred (NoSuchKey) when calling the GetObject operation', True), ('AccessDenied', False), ('network timeout', False)]:
                run.return_value = subprocess.CompletedProcess([], 1, '', error)
                if missing:
                    self.assertFalse(release.download('ready.json', Path('unused'), allow_missing=True))
                else:
                    with self.assertRaises(RuntimeError):
                        release.download('ready.json', Path('unused'), allow_missing=True)

    def test_receipt_identity_must_match(self):
        record = {'version': '1.0.0', 'source': 'a' * 64, 'team_id': 'ABCDEFGHIJ'}
        with patch.object(release, 'receipt_tool', return_value=json.dumps(record)):
            self.assertEqual(release.checked_receipt(Path('unused'), '1.0.0', 'a' * 64, 'ABCDEFGHIJ'), record)
            with self.assertRaises(RuntimeError):
                release.checked_receipt(Path('unused'), '1.0.1', 'a' * 64, 'ABCDEFGHIJ')

    def test_artifact_hash_rejects_tampering(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / 'a.zip'
            self.archive(p)
            record = {'artifacts': {'amd64': {'size': p.stat().st_size, 'sha256': '0' * 64}}}
            with patch.object(release, 'download'):
                with self.assertRaisesRegex(RuntimeError, 'checksum'):
                    release.checked_artifact('prefix', 'amd64', p, record)

    def test_online_validation_retries_network_only(self):
        fail = subprocess.CalledProcessError(68, ['stapler'], stderr='NSURLErrorDomain Code=-1005 network connection was lost')
        with patch.object(release, 'command', side_effect=[fail, 'ok']) as cmd, patch.object(release.time, 'sleep') as sleep:
            self.assertEqual(release.retry_online(['stapler']), 'ok')
            self.assertEqual(cmd.call_count, 2)
            sleep.assert_called_once_with(5)
        fail = subprocess.CalledProcessError(68, ['stapler'], stderr='invalid ticket')
        with patch.object(release, 'command', side_effect=fail) as cmd, patch.object(release.time, 'sleep') as sleep:
            with self.assertRaisesRegex(RuntimeError, 'invalid ticket'):
                release.retry_online(['stapler'])
            self.assertEqual(cmd.call_count, 1)
            sleep.assert_not_called()

    def test_restore_verifies_both_before_install_and_never_calls_online(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            args = SimpleNamespace(work_dir=root / 'state', helpers_dir=root / 'helpers', version='1.0.0', source='a'*64, team_id='ABCDEFGHIJ')
            record = {'artifacts': {}}
            def dl(key, path, **kwargs):
                if key.endswith('ready.json'):
                    path.write_text('{}')
                return True
            def artifact(prefix, arch, path, rec):
                path.write_bytes(b'zip')
            identity_calls = []
            def identity(app, version, team, **kwargs):
                self.assertNotIn('online', kwargs)
                identity_calls.append(str(app))
                if len(identity_calls) == 2:
                    raise RuntimeError('bad second signature')
            with patch.object(release, 'download', side_effect=dl), patch.object(release, 'checked_receipt', return_value=record), patch.object(release, 'checked_artifact', side_effect=artifact), patch.object(release, 'command'), patch.object(release, 'validate_identity', side_effect=identity):
                with self.assertRaisesRegex(RuntimeError, 'second'):
                    release.restore(args)
            self.assertFalse(args.helpers_dir.exists())

    def test_publish_resumes_signed_local_validation_and_receipt_is_last(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            args = SimpleNamespace(work_dir=root, helpers_dir=root / 'helpers', version='1.0.0', source='a'*64, team_id='ABCDEFGHIJ')
            record = {'artifacts': {}}
            (root / 'ready.json').write_text('signed')
            for arch in release.ARCHES:
                p = root / (arch + '.zip')
                self.archive(p)
                record['artifacts'][arch] = {'sha256': release.digest(p), 'size': p.stat().st_size}
            with patch.object(release, 'download', return_value=False), patch.object(release, 'checked_receipt', return_value=record), patch.object(release, 'validate_identity') as identity, patch.object(release, 'immutable_upload') as upload:
                self.assertEqual(release.publish(args), 0)
                identity.assert_not_called()
                self.assertTrue(upload.call_args_list[-1].args[0].endswith('/ready.json'))
                self.assertEqual(upload.call_count, 3)

    def native_archive(self, path, timestamp, *, body=b'binary', mode=0o100755, ticket=b'stapled-ticket'):
        with zipfile.ZipFile(path, 'w') as archive:
            for name, contents, permissions in (
                (release.APP + '/Contents/Info.plist', b'<plist/>', 0o100644),
                (release.APP + '/Contents/MacOS/EigenFluxNotifications', body, mode),
                ('__MACOSX/' + release.APP + '/Contents/._CodeResources', ticket, 0o100644),
            ):
                info = zipfile.ZipInfo(name, timestamp)
                info.create_system = 3
                info.external_attr = permissions << 16
                archive.writestr(info, contents)

    def test_partial_upload_adopts_existing_bytes_when_only_zip_metadata_differs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old, current = root / 'old.zip', root / 'current.zip'
            self.native_archive(old, (2020, 1, 1, 0, 0, 0))
            self.native_archive(current, (2026, 11, 2, 0, 0, 0))
            self.assertNotEqual(old.read_bytes(), current.read_bytes())
            def download(key, destination, **kwargs):
                destination.write_bytes(old.read_bytes())
                return True
            with patch.object(release, 'download', side_effect=download):
                release.adopt_uploaded_archive('notifier/version/source', 'amd64', current)
            self.assertEqual(current.read_bytes(), old.read_bytes())

    def test_partial_upload_rejects_payload_permission_and_ticket_changes(self):
        for changes in ({'body': b'different'}, {'mode': 0o100644}, {'ticket': b'different-ticket'}):
            with self.subTest(changes=changes), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                old, current = root / 'old.zip', root / 'current.zip'
                self.native_archive(old, (2020, 1, 1, 0, 0, 0), **changes)
                self.native_archive(current, (2026, 11, 2, 0, 0, 0))
                original = current.read_bytes()
                def download(key, destination, **kwargs):
                    destination.write_bytes(old.read_bytes())
                    return True
                with patch.object(release, 'download', side_effect=download):
                    with self.assertRaisesRegex(RuntimeError, 'payload differs'):
                        release.adopt_uploaded_archive('notifier/version/source', 'amd64', current)
                self.assertEqual(current.read_bytes(), original)

    def test_fresh_runner_completes_release_after_one_architecture_was_uploaded(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            previous = root / 'previous.zip'
            self.native_archive(previous, (2020, 1, 1, 0, 0, 0))
            prefix = 'notifier/1.0.0/' + 'a' * 64
            stored = {prefix + '/amd64.zip': previous.read_bytes()}
            uploaded = []
            args = SimpleNamespace(work_dir=root / 'fresh-state', helpers_dir=root / 'helpers',
                                   version='1.0.0', source='a' * 64, team_id='ABCDEFGHIJ')
            def download(key, destination, *, allow_missing=False):
                if key not in stored:
                    self.assertTrue(allow_missing)
                    return False
                destination.write_bytes(stored[key])
                return True
            def upload(key, path):
                value = path.read_bytes()
                if key in stored:
                    self.assertEqual(stored[key], value, 'immutable object must not be replaced')
                else:
                    stored[key] = value
                uploaded.append(key)
            def command(args, **kwargs):
                self.assertEqual(args[:4], ['/usr/bin/ditto', '-c', '-k', '--keepParent'])
                self.native_archive(Path(args[-1]), (2026, 11, 2, 0, 0, 0))
            def receipt_tool(mode, path):
                data = json.loads(path.read_text())
                if mode == 'sign':
                    data['signature'] = 'fixture-signature'
                else:
                    self.assertEqual(data['signature'], 'fixture-signature')
                return json.dumps(data)
            with patch.object(release, 'download', side_effect=download), patch.object(release, 'immutable_upload', side_effect=upload), patch.object(release, 'command', side_effect=command), patch.object(release, 'receipt_tool', side_effect=receipt_tool), patch.object(release, 'validate_identity') as validate:
                self.assertEqual(release.publish(args), 0)
                self.assertEqual(validate.call_count, 2)
                self.assertTrue(all(call.kwargs['online'] for call in validate.call_args_list))
            receipt = json.loads(stored[prefix + '/ready.json'])
            self.assertEqual(uploaded[-1], prefix + '/ready.json')
            self.assertEqual(len(uploaded), 3)
            self.assertEqual(stored[prefix + '/amd64.zip'], previous.read_bytes())
            self.assertEqual(receipt['artifacts']['amd64']['sha256'], release.digest(previous))
            self.assertIn(prefix + '/arm64.zip', stored)

    def test_immutable_collision_cannot_replace_existing(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / 'new'
            p.write_bytes(b'new')
            def download(key, dest):
                dest.write_bytes(b'old')
            with patch.dict(os.environ, {'R2_ENDPOINT': 'https://storage.test', 'R2_BUCKET': 'test'}), patch.object(release.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, '', 'PreconditionFailed')), patch.object(release, 'download', side_effect=download):
                with self.assertRaisesRegex(RuntimeError, 'differs'):
                    release.immutable_upload('key', p)


if __name__ == '__main__':
    unittest.main()
