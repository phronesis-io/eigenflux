"""Exercise version selection against real Git history and a fake R2 origin."""
from contextlib import redirect_stdout
import hashlib
import io
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import MagicMock, patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("publisher", ROOT / "scripts/release/publish-installer.py")
publisher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publisher)


class PublishInstallerTest(unittest.TestCase):
    def setUp(self):
        self.enterContext(redirect_stdout(io.StringIO()))
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "static").mkdir()
        self.source = self.root / "static/install.sh"
        self.content = (b'#!/bin/sh\nINSTALLER_VERSION="0.0.0-dev"\n'
                        b'INSTALLER_SOURCE_COMMIT="development"\nprintf "test\\n"\n')
        self.objects = {}
        self.uploaded = []
        self.corrupt_cdn = False
        self.cache = "no-store"
        self.enterContext(patch.dict(os.environ, {
            "R2_ENDPOINT": "https://r2.example", "R2_BUCKET": "test",
            "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test",
        }))
        self.enterContext(patch.dict(os.environ, {"GITHUB_STEP_SUMMARY": ""}))
        self.git("init", "-q")
        self.commit(self.content)
        self.aws = self.enterContext(patch.object(publisher, "aws", side_effect=self.storage))
        self.open = self.enterContext(patch.object(publisher, "urlopen", side_effect=self.public))
        self.enterContext(patch.object(publisher.time, "sleep"))

    def git(self, *args):
        return subprocess.check_output(
            ["git", "-C", str(self.root), "-c", "user.name=Test", "-c", "user.email=test@example.com",
             "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", *args], stderr=subprocess.PIPE)

    def commit(self, content):
        self.source.write_bytes(content)
        self.git("add", "static/install.sh")
        self.git("commit", "-qm", "installer change")
        return self.git("rev-parse", "HEAD").decode().strip()

    def storage(self, operation, *args):
        key = args[args.index("--key") + 1]
        self.assertEqual(key, publisher.LATEST_KEY, "only latest may be accessed")
        if operation == "put-object":
            if key in self.objects:
                self.assertEqual(args[args.index("--if-match") + 1], self.objects[key][1]["ETag"])
            else:
                self.assertEqual(args[args.index("--if-none-match") + 1], "*")
            body = Path(args[args.index("--body") + 1]).read_bytes()
            self.objects[key] = (body, {
                "ContentType": args[args.index("--content-type") + 1],
                "CacheControl": args[args.index("--cache-control") + 1],
                "ETag": '"' + hashlib.sha256(body).hexdigest() + '"',
            })
            self.uploaded.append(key)
            return {}
        if key not in self.objects:
            raise subprocess.CalledProcessError(254, "aws", stderr="An error occurred (NoSuchKey) when calling the GetObject operation")
        body, metadata = self.objects[key]
        Path(args[args.index("--key") + 2]).write_bytes(body)
        return metadata

    def public(self, url, timeout):
        self.assertEqual(url, f"{publisher.CDN}/{publisher.LATEST_KEY}")
        response = MagicMock()
        response.__enter__.return_value.read.return_value = b"stale" if self.corrupt_cdn else self.objects[publisher.LATEST_KEY][0]
        response.__enter__.return_value.headers = {"Cache-Control": self.cache}
        return response

    def publish(self):
        return publisher.publish(self.source, self.root / "build/release.json")

    def test_first_release_and_identical_retry_write_only_once(self):
        first = self.publish()
        self.assertEqual(first["version"], "0.1.0")
        self.assertEqual(first, self.publish())
        self.assertEqual(len(self.uploaded), 1)
        self.assertEqual(self.source.read_bytes(), self.content)
        self.assertEqual(json.loads((self.root / "build/release.json").read_text()), first)
        self.assertEqual(set(self.objects), {publisher.LATEST_KEY})

    def test_script_change_automatically_increments_patch(self):
        self.publish()
        self.commit(self.content + b"# changed\n")
        self.assertEqual(self.publish()["version"], "0.1.1")
        self.assertEqual(len(self.objects), 1)

    def test_unrelated_main_commit_does_not_increment(self):
        first = self.publish()
        self.git("commit", "--allow-empty", "-qm", "unrelated change")
        self.assertEqual(first, self.publish())
        self.assertEqual(len(self.uploaded), 1)

    def test_parallel_branch_merge_uses_main_merge_identity(self):
        base = self.git("rev-parse", "HEAD").decode().strip()
        main = self.git("symbolic-ref", "--short", "HEAD").decode().strip()
        self.commit(self.content + b"# change A\n")
        first = self.publish()
        self.git("checkout", "-qb", "parallel", base)
        branch_commit = self.commit(self.content + b"# change A\n# change B\n")
        self.git("checkout", "-q", main)
        self.git("merge", "--no-ff", "-X", "theirs", "-m", "merge parallel installer", "parallel")
        merge_commit = self.git("rev-parse", "HEAD").decode().strip()
        # Path-simplified history chooses B, although A is not its ancestor.
        self.assertEqual(self.git("log", "-1", "--format=%H", "--", "static/install.sh").decode().strip(), branch_commit)
        self.git("merge-base", "--is-ancestor", first["source_commit"], merge_commit)
        result = self.publish()
        self.assertEqual(result["source_commit"], merge_commit)
        self.assertEqual(result["version"], "0.1.1")
        self.assertEqual(self.publish(), result)
        self.assertEqual(len(self.uploaded), 2)

    def test_unrelated_merge_does_not_allocate_a_version(self):
        main = self.git("symbolic-ref", "--short", "HEAD").decode().strip()
        first = self.publish()
        self.git("checkout", "-qb", "unrelated")
        (self.root / "README.md").write_text("Unrelated change\n")
        self.git("add", "README.md")
        self.git("commit", "-qm", "unrelated docs")
        self.git("checkout", "-q", main)
        self.git("merge", "--no-ff", "-m", "merge docs", "unrelated")
        self.assertEqual(self.publish(), first)
        self.assertEqual(len(self.uploaded), 1)

    def test_manual_version_then_unchanged_manual_field_auto_increments(self):
        self.publish()
        explicit = self.content.replace(b"0.0.0-dev", b"1.2.0")
        self.commit(explicit)
        self.assertEqual(self.publish()["version"], "1.2.0")
        self.commit(explicit + b"# new behavior\n")
        self.assertEqual(self.publish()["version"], "1.2.1")
        self.commit(self.content + b"# resume dev marker\n")
        self.assertEqual(self.publish()["version"], "1.2.2")

    def test_first_release_can_request_specific_version(self):
        self.commit(self.content.replace(b"0.0.0-dev", b"2.0.0"))
        self.assertEqual(self.publish()["version"], "2.0.0")

    def test_manual_same_or_lower_version_is_rejected(self):
        self.publish()
        for value in (b"0.1.0", b"0.0.9"):
            self.commit(self.content.replace(b"0.0.0-dev", value))
            with self.assertRaisesRegex(ValueError, "must exceed"):
                self.publish()
        self.assertEqual(len(self.uploaded), 1)

    def test_malformed_manual_version_is_rejected(self):
        self.commit(self.content.replace(b"0.0.0-dev", b"01.2.0"))
        with self.assertRaisesRegex(ValueError, "Invalid installer version"):
            self.publish()
        self.assertFalse(self.uploaded)

    def test_dirty_source_is_rejected(self):
        self.source.write_bytes(self.content + b"# uncommitted\n")
        with self.assertRaisesRegex(ValueError, "committed checkout"):
            self.publish()
        self.aws.assert_not_called()

    def test_invalid_script_does_not_access_r2(self):
        self.source.write_bytes(b"#!/bin/sh\nif then\n")
        with self.assertRaises(subprocess.CalledProcessError):
            self.publish()
        self.aws.assert_not_called()

    def test_empty_source_does_not_access_r2(self):
        self.source.write_bytes(b"")
        with self.assertRaises(ValueError):
            self.publish()
        self.aws.assert_not_called()

    def test_missing_credentials_does_not_access_r2(self):
        with patch.dict(os.environ, {"R2_BUCKET": ""}):
            with self.assertRaises(ValueError):
                self.publish()
        self.aws.assert_not_called()

    def test_origin_errors_are_not_treated_as_first_release(self):
        for code in ("AccessDenied", "NoSuchBucket", "InternalError"):
            self.aws.side_effect = subprocess.CalledProcessError(1, "aws", stderr=f"({code})")
            with self.assertRaises(subprocess.CalledProcessError):
                self.publish()
        self.assertFalse(self.uploaded)

    def test_stale_cdn_retry_reuses_uploaded_version(self):
        self.corrupt_cdn = True
        with self.assertRaisesRegex(RuntimeError, "CDN content"):
            self.publish()
        self.assertEqual(self.open.call_count, 3)
        self.assertFalse(json.loads((self.root / "build/release.json").read_text())["verified"])
        self.corrupt_cdn = False
        self.assertEqual(self.publish()["version"], "0.1.0")
        self.assertEqual(len(self.uploaded), 1)

    def test_latest_cache_misconfiguration_fails(self):
        self.cache = "public, max-age=3600"
        with self.assertRaisesRegex(RuntimeError, "no-store"):
            self.publish()

    def test_corrupted_origin_blocks_next_release(self):
        self.publish()
        body, metadata = self.objects[publisher.LATEST_KEY]
        self.objects[publisher.LATEST_KEY] = (body + b"# tampered\n", metadata)
        self.commit(self.content + b"# change\n")
        with self.assertRaisesRegex(ValueError, "does not match its Git source"):
            self.publish()
        self.assertEqual(len(self.uploaded), 1)

    def test_stale_checkout_cannot_roll_back_latest(self):
        first_commit = self.git("rev-parse", "HEAD").decode().strip()
        self.publish()
        self.commit(self.content + b"# new\n")
        self.publish()
        self.git("checkout", "-q", first_commit)
        with self.assertRaises(subprocess.CalledProcessError):
            self.publish()
        self.assertEqual(len(self.uploaded), 2)

    def test_revert_publishes_old_behavior_under_new_version(self):
        self.publish()
        self.commit(self.content + b"# new\n")
        self.publish()
        self.commit(self.content)
        self.assertEqual(self.publish()["version"], "0.1.2")

    def test_conditional_write_conflict_does_not_retry_overwrite(self):
        def conflict(operation, *args):
            if operation == "put-object":
                raise subprocess.CalledProcessError(1, "aws", stderr="(PreconditionFailed)")
            return self.storage(operation, *args)
        self.aws.side_effect = conflict
        with self.assertRaises(subprocess.CalledProcessError):
            self.publish()
        self.open.assert_not_called()
        self.assertFalse(self.uploaded)

    def test_upload_succeeds_but_client_times_out_retry_does_not_increment(self):
        def timeout(operation, *args):
            result = self.storage(operation, *args)
            if operation == "put-object":
                raise subprocess.TimeoutExpired("aws", 45)
            return result
        self.aws.side_effect = timeout
        with self.assertRaises(subprocess.TimeoutExpired):
            self.publish()
        self.aws.side_effect = self.storage
        self.assertEqual(self.publish()["version"], "0.1.0")
        self.assertEqual(len(self.uploaded), 1)

    def test_missing_or_duplicate_markers_do_not_publish(self):
        for content in (self.content.replace(b'INSTALLER_VERSION="0.0.0-dev"\n', b""),
                        self.content + b'INSTALLER_VERSION="0.0.0-dev"\n'):
            self.commit(content)
            with self.assertRaises(ValueError):
                self.publish()
        self.assertFalse(self.uploaded)


class InstallerVersionTest(unittest.TestCase):
    def test_version_exits_without_external_commands_or_files(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = (ROOT / "static/install.sh").read_bytes()
            for content, expected in ((source, "0.0.0-dev"),
                                      (publisher.render(source, "1.2.3", "a" * 40), "1.2.3")):
                script = root / "install.sh"
                script.write_bytes(content)
                result = subprocess.run(["/bin/sh", str(script), "--version"],
                                        env={"HOME": temp, "PATH": "/nonexistent"},
                                        capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(f"eigenflux-installer {expected}", result.stdout)
                self.assertEqual(list(root.iterdir()), [script])
