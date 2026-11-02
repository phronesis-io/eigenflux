import importlib.util
import json
import hashlib
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('checkpoint', ROOT / 'cli/scripts/notifier-checkpoint.py')
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

class CheckpointTests(unittest.TestCase):
    def test_retry_only_network_failure(self):
        network = subprocess.CompletedProcess([], 1, '', 'NSURLErrorDomain -1005 connection was lost')
        good = subprocess.CompletedProcess([], 0, 'ok', '')
        with patch.object(m.subprocess, 'run', side_effect=[network, good]) as run, patch.object(m.time, 'sleep'):
            self.assertEqual(m.run(['test'], retry=True), 'ok')
            self.assertEqual(run.call_count, 2)
        with patch.object(m.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, '', 'signature rejected')) as run:
            with self.assertRaises(RuntimeError): m.run(['test'], retry=True)
            self.assertEqual(run.call_count, 1)

    def test_download_does_not_treat_network_as_missing(self):
        with patch.object(m, 'aws', return_value=subprocess.CompletedProcess([], 1, '', 'connection lost')):
            with self.assertRaises(RuntimeError): m.download('key', Path('unused'))
        with patch.object(m, 'aws', return_value=subprocess.CompletedProcess([], 1, '', 'An error occurred (NoSuchKey) when calling the GetObject operation')):
            self.assertFalse(m.download('key', Path('unused')))

    def test_reuses_recorded_submission_without_submit(self):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            app, signed = work/'App', work/'signed.zip'
            signed.write_bytes(b'signed-archive')
            calls=[]
            def download(key, path):
                path.write_text(json.dumps({'id':'12345678-1234-1234-1234-123456789abc','signed_sha256':hashlib.sha256(b'signed-archive').hexdigest()}))
                return True
            def run(args, retry=False):
                calls.append([str(x) for x in args])
                return '{"status":"Accepted"}' if 'wait' in args else ''
            with patch.object(m, 'prepare', return_value=(app,signed,'prefix',work)), patch.object(m,'download',side_effect=download), patch.object(m,'run',side_effect=run), patch.dict(m.os.environ, {'EIGENFLUX_NOTARY_PROFILE':'test'}):
                m.notarize(None,'arm64')
            self.assertFalse(any('submit' in c for c in calls))
            self.assertTrue(any('wait' in c for c in calls))
            self.assertTrue(any('staple' in c for c in calls))

    def test_pending_wait_does_not_staple_or_resubmit(self):
        with tempfile.TemporaryDirectory() as tmp:
            work=Path(tmp)
            (work/'signed.zip').write_bytes(b'signed-archive')
            def download(key,path):
                path.write_text(json.dumps({'id':'12345678-1234-1234-1234-123456789abc','signed_sha256':hashlib.sha256(b'signed-archive').hexdigest()}))
                return True
            with patch.object(m,'prepare',return_value=(work/'App',work/'signed.zip','prefix',work)), patch.object(m,'download',side_effect=download), patch.object(m,'run',return_value='{"status":"Invalid"}') as run, patch.dict(m.os.environ, {'EIGENFLUX_NOTARY_PROFILE':'test'}):
                with self.assertRaises(RuntimeError):m.notarize(None,'amd64')
                self.assertEqual(run.call_count,1)


    def test_local_submission_survives_remote_checkpoint_upload_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            signed = work / 'signed.zip'
            signed.write_bytes(b'signed-archive')
            (work / 'submission.json').write_text(json.dumps({
                'id': '12345678-1234-1234-1234-123456789abc',
                'signed_sha256': m._store.digest(signed),
            }))
            with patch.object(m, 'prepare', return_value=(work/'App', signed, 'prefix', work)), patch.object(m, 'download', return_value=False), patch.object(m, 'put_new') as upload, patch.object(m, 'run', return_value='{"status":"Accepted"}') as run, patch.dict(m.os.environ, {'EIGENFLUX_NOTARY_PROFILE':'test'}):
                m.notarize(None, 'amd64')
                upload.assert_called_once()
                self.assertFalse(any('submit' in call.args[0] for call in run.call_args_list))
                self.assertTrue(any('wait' in call.args[0] for call in run.call_args_list))

    def test_submission_checkpoint_binds_archive_and_requires_real_uuid(self):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            signed = work / 'signed.zip'
            signed.write_bytes(b'signed-archive')
            checkpoint = work / 'submission.json'
            for identifier, digest in (
                ('-' * 36, m._store.digest(signed)),
                ('12345678-1234-1234-1234-123456789abc', '0' * 64),
            ):
                checkpoint.write_text(json.dumps({'id': identifier, 'signed_sha256': digest}))
                with self.assertRaises(RuntimeError):
                    m.submission_id(checkpoint, signed)

    def test_invalid_submit_response_is_not_persisted(self):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            signed = work / 'signed.zip'
            signed.write_bytes(b'signed-archive')
            with patch.object(m, 'prepare', return_value=(work/'App', signed, 'prefix', work)), patch.object(m, 'download', return_value=False), patch.object(m, 'put_new') as upload, patch.object(m, 'run', return_value='{"id":"invalid"}'), patch.dict(m.os.environ, {'EIGENFLUX_NOTARY_PROFILE':'test'}):
                with self.assertRaises(RuntimeError):
                    m.notarize(None, 'amd64')
                upload.assert_not_called()
                self.assertFalse((work / 'submission.json').exists())
