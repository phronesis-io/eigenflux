import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import MagicMock, patch


ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("install_doc", ROOT / "cli/scripts/publish-install-doc.py")
install_doc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(install_doc)


class InstallDocumentTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.source = Path(self.temp.name) / "install.md"
        self.content = "# Install EigenFlux\r\n\nUse the current Agent. 安装\n".encode()
        self.source.write_bytes(self.content)
        env = patch.dict(os.environ, {
            "R2_ENDPOINT": "https://r2.example", "R2_BUCKET": "skills",
            "AWS_ACCESS_KEY_ID": "test-key", "AWS_SECRET_ACCESS_KEY": "test-secret",
        }, clear=True)
        env.start()
        self.addCleanup(env.stop)
        self.run = patch.object(install_doc.subprocess, "run", side_effect=self.aws).start()
        self.open = patch.object(install_doc, "urlopen").start()
        self.sleep = patch.object(install_doc.time, "sleep").start()
        self.addCleanup(patch.stopall)
        self.origin_content = self.content
        self.metadata = {"ContentType": install_doc.CONTENT_TYPE, "CacheControl": "no-store"}
        self.response(self.content)

    def aws(self, command, **kwargs):
        self.assertEqual(kwargs["timeout"], 45)
        self.assertIn("--cli-connect-timeout", command)
        self.assertIn("--cli-read-timeout", command)
        self.assertEqual(command[command.index("--key") + 1], "skills/latest/install.md")
        if "put-object" in command:
            path = Path(command[command.index("--body") + 1])
            self.assertEqual(path.read_bytes(), self.content)
            self.assertEqual(command[command.index("--content-type") + 1], "text/markdown; charset=utf-8")
            self.assertEqual(command[command.index("--cache-control") + 1], "no-store")
            return subprocess.CompletedProcess(command, 0, "{}", "")
        path = Path(command[command.index("--key") + 2])
        path.write_bytes(self.origin_content)
        return subprocess.CompletedProcess(command, 0, json.dumps(self.metadata), "")

    def response(self, *contents):
        responses = []
        for content in contents:
            context = MagicMock()
            context.__enter__.return_value.headers = {"Cache-Control": "no-store"}
            context.__enter__.return_value.read.return_value = content
            responses.append(context)
        self.open.side_effect = responses

    def test_publishes_original_bytes_and_verifies_exact_public_url(self):
        install_doc.publish(self.source)
        self.assertEqual(self.run.call_count, 2)
        self.open.assert_called_once_with("https://cdn.eigenflux.ai/skills/latest/install.md", timeout=15)
        self.sleep.assert_not_called()

    def test_install_document_change_alone_is_published(self):
        install_doc.publish(self.source)
        self.content = b"# Updated installation contract\n"
        self.origin_content = self.content
        self.source.write_bytes(self.content)
        self.response(self.content)
        install_doc.publish(self.source)
        self.assertEqual(sum("put-object" in call.args[0] for call in self.run.call_args_list), 2)

    def test_uses_cdn_override_without_cache_busting(self):
        with patch.dict(os.environ, {"EIGENFLUX_CDN": "https://mirror.example/"}):
            install_doc.publish(self.source)
        self.open.assert_called_once_with("https://mirror.example/skills/latest/install.md", timeout=15)

    def test_missing_source_does_not_upload(self):
        with self.assertRaises(FileNotFoundError):
            install_doc.publish(self.source.with_name("missing.md"))
        self.run.assert_not_called()
        self.open.assert_not_called()

    def test_upload_failure_does_not_verify_or_retry(self):
        self.run.side_effect = subprocess.CalledProcessError(1, "aws", stderr="access denied")
        with self.assertRaisesRegex(RuntimeError, "put-object failed: access denied"):
            install_doc.publish(self.source)
        self.assertEqual(self.run.call_count, 1)
        self.open.assert_not_called()
        self.sleep.assert_not_called()

    def test_upload_timeout_fails(self):
        self.run.side_effect = subprocess.TimeoutExpired("aws", 45)
        with self.assertRaisesRegex(RuntimeError, "put-object timed out"):
            install_doc.publish(self.source)
        self.open.assert_not_called()

    def test_stale_cdn_fails_after_bounded_retries(self):
        self.response(b"old", b"old", b"old")
        with self.assertRaisesRegex(RuntimeError, "failed after 3 attempts: CDN"):
            install_doc.publish(self.source)
        self.assertEqual(self.open.call_count, 3)
        self.assertEqual(self.sleep.call_count, 2)
        self.assertTrue(all(call.args[0] == "https://cdn.eigenflux.ai/skills/latest/install.md"
                            for call in self.open.call_args_list))

    def test_stale_cdn_recovers_on_retry(self):
        self.response(b"old", self.content)
        install_doc.publish(self.source)
        self.assertEqual(self.open.call_count, 2)
        self.sleep.assert_called_once_with(5)
        self.assertEqual(sum("put-object" in call.args[0] for call in self.run.call_args_list), 1)

    def test_matching_cdn_content_with_cacheable_headers_fails(self):
        response = MagicMock()
        response.__enter__.return_value.headers = {"Cache-Control": "public, max-age=3600"}
        response.__enter__.return_value.read.return_value = self.content
        self.open.side_effect = [response] * 3
        with self.assertRaisesRegex(RuntimeError, "must return Cache-Control: no-store"):
            install_doc.publish(self.source)
        self.assertEqual(self.open.call_count, 3)

    def test_origin_content_difference_prevents_cdn_verification(self):
        self.origin_content = b"unexpected content"
        with self.assertRaisesRegex(RuntimeError, "R2 installation document content or metadata"):
            install_doc.publish(self.source)
        self.open.assert_not_called()

    def test_origin_metadata_must_match(self):
        for field, value in (("ContentType", "text/plain"), ("CacheControl", "max-age=3600")):
            with self.subTest(field=field):
                original = self.metadata[field]
                self.metadata[field] = value
                with self.assertRaisesRegex(RuntimeError, "R2 installation document content or metadata"):
                    install_doc.publish(self.source)
                self.metadata[field] = original
        self.open.assert_not_called()

    def test_cdn_timeout_retries_and_fails(self):
        self.open.side_effect = TimeoutError("request timed out")
        with self.assertRaisesRegex(RuntimeError, "failed after 3 attempts: request timed out"):
            install_doc.publish(self.source)
        self.assertEqual(self.open.call_count, 3)

    def test_main_returns_nonzero_on_publish_failure(self):
        with patch.object(install_doc, "publish", side_effect=RuntimeError("upload failed")):
            self.assertEqual(install_doc.main(), 1)


if __name__ == "__main__":
    unittest.main()
