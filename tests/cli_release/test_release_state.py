import importlib.util
import json
import os
import shutil
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("release_state", ROOT / "cli/scripts/skills-release-state.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class SequenceTests(unittest.TestCase):
    def test_conflicting_legacy_eight_recovers_at_nine(self):
        records = [{"sequence": 8, "revision": "old"}, {"sequence": 8, "revision": "new"}]
        self.assertEqual(release.next_sequence(records), 9)
        release.require_transition({"sequence": 9}, records, 9)
        for candidate in (7, 8, 10):
            with self.assertRaises(RuntimeError):
                release.require_transition({"sequence": candidate}, records, 9)

    def test_partial_release_consumes_reserved_number(self):
        records = [{"sequence": 8}, {"sequence": 8}, {"sequence": 9}]
        self.assertEqual(release.next_sequence(records), 10)
        with self.assertRaises(RuntimeError):
            release.require_transition({"sequence": 9}, records, 9)

    def test_internal_phases_cannot_dispatch_a_local_publication(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            gh = root / 'gh'
            gh.write_text('#!/bin/sh\necho unexpected-dispatch >&2; exit 99\n')
            gh.chmod(0o755)
            env = dict(os.environ, GITHUB_ACTIONS='false', PATH=str(root) + os.pathsep + os.environ['PATH'])
            for phase in ('--prepare', '--publish'):
                result = subprocess.run(['bash', str(ROOT / 'cli/scripts/release-skills.sh'), phase], env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 1)
                self.assertIn('require the main workflow', result.stderr)
                self.assertNotIn('unexpected-dispatch', result.stderr)

    def test_sequence_overflow_fails_closed(self):
        with self.assertRaises(RuntimeError):
            release.next_sequence([{"sequence": 2**64 - 1}])

    def test_archive_uses_numeric_max_and_only_manifest_keys(self):
        objects = {"Contents": [{"Key": key} for key in (
            "skills/releases/9/manifest.json", "skills/releases/10/manifest.json",
            "skills/releases/999/skills.tar.gz", "skills/releases/bogus/manifest.json") ]}
        with patch.object(release, "aws", return_value=objects):
            self.assertEqual(release.archive_head(), 10)

    def test_snapshot_fails_on_invalid_signature_or_network_error(self):
        with tempfile.TemporaryDirectory() as temp:
            for failure in (RuntimeError("bad signature"), subprocess.CalledProcessError(1, "aws")):
                with patch.object(release, "aws"), patch.object(release, "verified", side_effect=failure):
                    with self.assertRaises(type(failure)):
                        release.snapshot(Path(temp), Path("checker"))

    def test_archive_sequence_must_match_signed_record(self):
        with tempfile.TemporaryDirectory() as temp:
            with patch.object(release, "aws"), patch.object(release, "archive_head", return_value=10), patch.object(release, "verified", return_value={"sequence": 8}):
                with self.assertRaisesRegex(RuntimeError, "disagree"):
                    release.snapshot(Path(temp), Path("checker"))

    def test_reservation_collision_stops_before_archive_upload(self):
        with tempfile.TemporaryDirectory() as temp:
            build = Path(temp)
            state = build / "release-state"
            state.mkdir()
            records = [{"sequence": 8}]
            (state / "plan.json").write_text(json.dumps({"sequence": 9, "records": records}))
            (build / "skills.tar.gz").write_bytes(b"archive")
            digest = release.hashlib.sha256(b"archive").hexdigest()
            (build / "skills.tar.gz.sha256").write_text(digest)
            candidate = {"sequence": 9, "tar_sha256": digest}
            with patch.object(release, "verified", return_value=candidate), patch.object(release, "snapshot", return_value=records), patch.object(release, "aws", side_effect=subprocess.CalledProcessError(1, "aws")) as aws:
                with self.assertRaises(subprocess.CalledProcessError):
                    release.reserve(state, build / "checker", build)
                self.assertEqual(aws.call_count, 1)
                args = aws.call_args.args
                self.assertIn("skills/releases/9/manifest.json", args)
                self.assertEqual(args[-2:], ("--if-none-match", "*"))

    def test_changed_origin_prevents_reservation(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)
            (state / "plan.json").write_text(json.dumps({"sequence": 9, "records": [{"sequence": 8, "revision": "old"}]}))
            with patch.object(release, "verified", return_value={"sequence": 9}), patch.object(release, "snapshot", return_value=[{"sequence": 8, "revision": "changed"}]), patch.object(release, "aws") as aws:
                with self.assertRaisesRegex(RuntimeError, "changed"):
                    release.reserve(state, state / "checker", state)
                aws.assert_not_called()

    def test_successful_reservation_archives_manifest_before_payload(self):
        with tempfile.TemporaryDirectory() as temp:
            build = Path(temp)
            records = [{"sequence": 8}]
            (build / "plan.json").write_text(json.dumps({"sequence": 9, "records": records}))
            (build / "skills.tar.gz").write_bytes(b"archive")
            digest = release.hashlib.sha256(b"archive").hexdigest()
            (build / "skills.tar.gz.sha256").write_text(digest)
            with patch.object(release, "verified", return_value={"sequence": 9, "tar_sha256": digest}), patch.object(release, "snapshot", return_value=records), patch.object(release, "put_new") as put:
                release.reserve(build, build / "checker", build)
                self.assertEqual([call.args[1] for call in put.call_args_list], [
                    "skills/releases/9/manifest.json", "skills/releases/9/skills.tar.gz",
                    "skills/releases/9/skills.tar.gz.sha256"])

    def release_record(self, sequence=8):
        return {"sequence": sequence, "signature": f"signature-{sequence}", "generated_at": sequence,
                "revision": "same-content", "tar_sha256": release.hashlib.sha256(b"archive").hexdigest(),
                "key_id": "trusted", "managed_by": "eigenflux-cli", "cli_version": "1.0.5",
                "min_cli_version": "1.0.3", "skills": [{"name": "ef-test", "sha256": "hash"}]}

    def test_unchanged_release_reuses_trusted_sequence_without_writes(self):
        with tempfile.TemporaryDirectory() as temp:
            build = Path(temp)
            old, candidate = self.release_record(8), self.release_record(9)
            candidate["cli_version"] = "1.0.6"
            records = [old, old, old]
            (build / "plan.json").write_text(json.dumps({"sequence": 9, "records": records}))
            (build / "manifest.json").write_text(json.dumps(candidate))
            previous = json.dumps(old)
            (build / "previous-0.json").write_text(previous)
            (build / "skills.tar.gz").write_bytes(b"archive")
            (build / "skills.tar.gz.sha256").write_text(candidate["tar_sha256"])
            def origin(*args):
                self.assertEqual(args[0], "get-object")
                Path(args[-1]).write_bytes(b"archive" if args[-2].endswith('.tar.gz') else candidate["tar_sha256"].encode())
            with patch.object(release, "verified", return_value=candidate), patch.object(release, "snapshot", return_value=records), patch.object(release, "aws", side_effect=origin) as aws, patch.object(release, "put_new") as put:
                release.reserve(build, build / "checker", build)
                put.assert_not_called()
                self.assertEqual(aws.call_count, 4)
            self.assertEqual((build / "manifest.json").read_text(), previous)
            self.assertEqual(json.loads((build / "publication.json").read_text()), {"action": "noop", "sequence": 8})

    def test_noop_never_ignores_compatibility_identity_or_skill_metadata(self):
        old = self.release_record(8)
        for field, value in (("min_cli_version", "1.0.6"), ("key_id", "other"),
                             ("skills", [{"name": "ef-other", "sha256": "hash"}]), ("tar_sha256", "other")):
            with self.subTest(field=field):
                candidate = self.release_record(9)
                candidate[field] = value
                self.assertFalse(release.same_release(candidate, old))

    def test_unchanged_source_payload_corruption_fails_before_noop(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)
            def origin(*args):
                Path(args[-1]).write_bytes(b"corrupt")
            with patch.object(release, "aws", side_effect=origin):
                with self.assertRaisesRegex(RuntimeError, "differs"):
                    release.verify_origin_payloads(state, self.release_record())

    def test_exact_prepared_release_resumes_reserved_archive(self):
        with tempfile.TemporaryDirectory() as temp:
            build = Path(temp)
            old, candidate = self.release_record(8), self.release_record(9)
            original = json.dumps(candidate)
            (build / "plan.json").write_text(json.dumps({"sequence": 9, "records": [old, old, old]}))
            (build / "manifest.json").write_text(original)
            (build / "archive-head.json").write_text(original)
            (build / "skills.tar.gz").write_bytes(b"archive")
            (build / "skills.tar.gz.sha256").write_text(candidate["tar_sha256"])
            # One pointer was promoted before interruption; the other is retained.
            records = [candidate, old, candidate]
            with patch.object(release, "verified", return_value=candidate), patch.object(release, "snapshot", return_value=records), patch.object(release, "put_new") as put:
                release.reserve(build, build / "checker", build)
                self.assertEqual(put.call_count, 3)
                self.assertEqual(json.loads((build / "publication.json").read_text())["action"], "publish")
            for head in (self.release_record(10), dict(candidate, signature="different-signature")):
                with patch.object(release, "verified", return_value=candidate), patch.object(release, "snapshot", return_value=[head, old, head]), patch.object(release, "put_new") as put:
                    with self.assertRaises(RuntimeError):
                        release.reserve(build, build / "checker", build)
                    put.assert_not_called()
            (build / "archive-head.json").write_text(original + "\n")
            with patch.object(release, "verified", return_value=candidate), patch.object(release, "snapshot", return_value=records), patch.object(release, "put_new") as put:
                with self.assertRaisesRegex(RuntimeError, "bytes differ"):
                    release.reserve(build, build / "checker", build)
                put.assert_not_called()

    def test_immutable_retry_accepts_only_exact_existing_bytes(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "archive"
            path.write_bytes(b"exact")
            for existing in (b"exact", b"different"):
                def aws(*args):
                    if args[0] == "put-object": raise RuntimeError("PreconditionFailed")
                    Path(args[-1]).write_bytes(existing)
                with patch.object(release, "aws", side_effect=aws):
                    if existing == b"exact": release.put_new(path, "key")
                    else:
                        with self.assertRaisesRegex(RuntimeError, "differs"):
                            release.put_new(path, "key")
            with patch.object(release, "aws", side_effect=RuntimeError("AccessDenied")) as aws:
                with self.assertRaisesRegex(RuntimeError, "AccessDenied"):
                    release.put_new(path, "key")
                self.assertEqual(aws.call_count, 1)

    def test_live_verification_rejects_mismatched_alias(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)
            with patch.object(release, "aws"), patch.object(release, "verified", side_effect=[{"sequence": 9}, {"sequence": 9}, {"sequence": 8}]):
                with self.assertRaisesRegex(RuntimeError, "differs"):
                    release.verify_live(state, state / "checker", state)

    def test_manual_release_dispatches_main_without_loading_credentials(self):
        with tempfile.TemporaryDirectory() as temp:
            fake_gh = Path(temp) / "gh"
            fake_gh.write_text('#!/bin/sh\nprintf "%s\\n" "$@"\n')
            fake_gh.chmod(0o700)
            result = subprocess.run(["/bin/bash", str(ROOT / "cli/scripts/release-skills.sh")], env={"PATH": temp + ":/usr/bin:/bin"}, check=True, capture_output=True, text=True)
            self.assertEqual(result.stdout.splitlines(), ["workflow", "run", "release-skills.yml", "--repo", "phronesis-io/eigenflux", "--ref", "main"])

    def test_prepare_and_unchanged_publish_need_no_origin_write(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            scripts, bindir = root / "cli/scripts", root / "bin"
            scripts.mkdir(parents=True)
            bindir.mkdir()
            for name in ("release-skills.sh", "skills-release-state.py"):
                shutil.copy(ROOT / "cli/scripts" / name, scripts / name)
            (root / "cli/.cli.config").write_text("CLI_VERSION=1.0.5\nSKILLS_MIN_CLI_VERSION=1.0.3\n")
            skill = root / "skills/ef-test"
            skill.mkdir(parents=True)
            (skill / "SKILL.md").write_text("fixture")
            (bindir / "go").write_text('''#!/usr/bin/env python3
import hashlib,json,pathlib,sys
args=sys.argv[1:]
if args[0]=='build':
 p=pathlib.Path(args[args.index('-o')+1]);p.write_text('#!/bin/sh\\nexit 0\\n');p.chmod(0o755)
elif '--print-public-key' in args: print('public')
elif '--print-allowlist' in args: print('ef-test')
else:
 out=pathlib.Path(args[args.index('--out')+1]);out.write_text(json.dumps({'sequence':int(args[args.index('--sequence')+1]),'revision':'aaaaaaaaaaaaaaaa','tar_sha256':hashlib.sha256(b'archive').hexdigest(),'key_id':'trusted','managed_by':'eigenflux-cli','cli_version':'1.0.5','min_cli_version':'1.0.3','skills':[]}))
''')
            (bindir / "gtar").write_text('''#!/usr/bin/env python3
import pathlib,sys
pathlib.Path(sys.argv[sys.argv.index('-czf')+1]).write_bytes(b'archive')
''')
            (bindir / "aws").write_text('''#!/usr/bin/env python3
import json,pathlib,sys,os
args=sys.argv[1:]
with open(os.environ['AWS_CALLS'],'a') as f:f.write(' '.join(args)+'\\n')
if 'get-object' in args:
 key=args[args.index('--key')+1];path=pathlib.Path(args[args.index('--key')+2])
 if key.endswith('manifest.json'):path.write_text(os.environ['ORIGIN_MANIFEST'])
 elif key.endswith('.tar.gz'):path.write_bytes(b'archive')
 else:path.write_text(json.loads(os.environ['ORIGIN_MANIFEST'])['tar_sha256'])
 print('{}')
elif 'list-objects-v2' in args:print('{"Contents":[]}')
else:raise SystemExit('prepare attempted a write')
''')
            for tool in bindir.iterdir(): tool.chmod(0o755)
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            subprocess.run(["git", "-C", str(root), "add", "."], check=True)
            subprocess.run(["git", "-C", str(root), "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture"], check=True)
            log = root / "aws-calls"
            env = dict(os.environ, PATH=str(bindir)+os.pathsep+os.environ['PATH'], GITHUB_ACTIONS="true",
                       GITHUB_WORKFLOW_REF="phronesis-io/eigenflux/.github/workflows/release-skills.yml@refs/heads/main",
                       R2_ACCESS_KEY_ID="test", R2_SECRET_ACCESS_KEY="test", R2_ENDPOINT="https://unused.invalid", R2_BUCKET="test",
                       EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY="public", EIGENFLUX_SKILLS_SIGNING_KEY_FILE="outside-artifact", AWS_CALLS=str(log),
                       ORIGIN_MANIFEST=json.dumps({'sequence':8,'revision':'aaaaaaaaaaaaaaaa','tar_sha256':release.hashlib.sha256(b'archive').hexdigest(),'key_id':'trusted','managed_by':'eigenflux-cli','cli_version':'1.0.5','min_cli_version':'1.0.3','skills':[]}))
            result = subprocess.run(["bash", str(scripts / "release-skills.sh"), "--prepare"], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout+result.stderr)
            self.assertIn("no R2 objects written", result.stdout)
            self.assertNotIn("put-object", log.read_text())
            self.assertNotIn("s3 cp", log.read_text())
            prepared = root / "build/cli"
            self.assertTrue((prepared / "prepared-source.json").is_file())
            self.assertTrue((prepared / "release-state/plan.json").is_file())
            for tool in ('manifestcheck','clirelease','releaseverify'):
                self.assertTrue(os.access(prepared / tool, os.X_OK))
            (bindir / "go").write_text('#!/bin/sh\necho unexpected compiler use >&2\nexit 1\n')
            (bindir / "curl").write_text('#!/bin/sh\nprintf archive\n')
            (bindir / "sleep").write_text('#!/bin/sh\nexit 0\n')
            for tool in ('curl','sleep'): (bindir / tool).chmod(0o755)
            (scripts / "publish-install-doc.py").write_text('from pathlib import Path\nPath(__file__).parents[2].joinpath("doc-refreshed").write_text("yes")\n')
            env.pop('EIGENFLUX_SKILLS_SIGNING_KEY_FILE')
            result = subprocess.run(["bash", str(scripts / "release-skills.sh"), "--publish"], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout+result.stderr)
            self.assertEqual(json.loads((prepared / 'release-state/publication.json').read_text())['action'], 'noop')
            self.assertEqual(json.loads((prepared / 'manifest.json').read_text())['sequence'], 8)
            self.assertTrue((root / 'doc-refreshed').is_file())
            self.assertNotIn('put-object', log.read_text())
            self.assertNotIn('s3 cp', log.read_text())

    def test_publish_phase_needs_no_private_key_and_rejects_wrong_artifact_source(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            scripts = root / "cli/scripts"
            scripts.mkdir(parents=True)
            shutil.copy(ROOT / "cli/scripts/release-skills.sh", scripts / "release-skills.sh")
            (root / "cli/.cli.config").write_text("CLI_VERSION=1.0.5\nSKILLS_MIN_CLI_VERSION=1.0.3\n")
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            subprocess.run(["git", "-C", str(root), "add", "."], check=True)
            subprocess.run(["git", "-C", str(root), "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture"], check=True)
            build = root / "build/cli"
            build.mkdir(parents=True)
            (build / "prepared-source.json").write_text(json.dumps({"sha": "wrong", "cli_version": "1.0.5", "min_cli_version": "1.0.3"}))
            env = dict(os.environ, GITHUB_ACTIONS="true", GITHUB_WORKFLOW_REF="phronesis-io/eigenflux/.github/workflows/release-skills.yml@refs/heads/main",
                       R2_ACCESS_KEY_ID="test", R2_SECRET_ACCESS_KEY="test", EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY="public-only")
            env.pop("EIGENFLUX_SKILLS_SIGNING_KEY_FILE", None)
            result = subprocess.run(["bash", str(scripts / "release-skills.sh"), "--publish"], env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("do not match", result.stderr)
            self.assertNotIn("signing key", result.stderr)

    def test_other_workflow_cannot_publish(self):
        result = subprocess.run(["/bin/bash", str(ROOT / "cli/scripts/release-skills.sh")], env={"PATH": "/usr/bin:/bin", "GITHUB_ACTIONS": "true", "GITHUB_WORKFLOW_REF": "other"}, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("requires the Release Skills workflow", result.stderr)


if __name__ == "__main__":
    unittest.main()
