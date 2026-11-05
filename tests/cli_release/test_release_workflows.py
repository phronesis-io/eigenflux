"""Check publication topology and artifact/credential boundaries from real YAML."""
import json
import os
import stat
import tempfile
from pathlib import Path
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[2]


def workflow(name):
    source = ROOT / '.github/workflows' / name
    result = subprocess.run(['ruby', '-ryaml', '-rjson', '-e',
                             'puts JSON.generate(YAML.load_file(ARGV[0]))', str(source)],
                            capture_output=True, text=True, check=True)
    return json.loads(result.stdout)


class ReleaseWorkflowTests(unittest.TestCase):
    def test_global_write_lock_does_not_cover_preparation(self):
        for name in ('release-cli.yml', 'release-skills.yml'):
            with self.subTest(workflow=name):
                data = workflow(name)
                self.assertNotEqual(data['concurrency']['group'], 'release-r2')
                self.assertFalse(data['concurrency']['cancel-in-progress'])
                self.assertNotIn('concurrency', data['jobs']['prepare'])
                self.assertEqual(data['jobs']['publish']['concurrency']['group'], 'release-r2')
                self.assertFalse(data['jobs']['publish']['concurrency']['cancel-in-progress'])
                needs = data['jobs']['publish']['needs']
                self.assertIn('prepare', [needs] if isinstance(needs, str) else needs)

    def test_current_run_artifacts_bind_prepare_to_publish(self):
        for name in ('release-cli.yml', 'release-skills.yml'):
            with self.subTest(workflow=name):
                jobs = workflow(name)['jobs']
                upload = next(s for s in jobs['prepare']['steps'] if 'upload-artifact@' in s.get('uses', ''))
                download = next(s for s in jobs['publish']['steps'] if 'download-artifact@' in s.get('uses', ''))
                self.assertEqual(upload['with']['name'], download['with']['name'])
                self.assertGreaterEqual(upload['with']['retention-days'], 14)
                self.assertNotIn('run-id', download['with'])
                self.assertNotIn('github-token', download['with'])
                self.assertTrue(upload['with']['path'].endswith('.tar'))
                self.assertFalse(any('setup-go@' in step.get('uses', '') for step in jobs['publish']['steps']))
                publish = json.dumps(jobs['publish'])
                for secret in ('SKILLS_SIGNING_KEY', 'MACOS_CERTIFICATE_PASSWORD', 'APPLE_APP_SPECIFIC_PASSWORD'):
                    self.assertNotIn(secret, publish)

    def test_cli_keeps_native_checks_and_authenticates_transferred_manifests(self):
        jobs = workflow('release-cli.yml')['jobs']
        native = next(s for s in jobs['prepare']['steps'] if s.get('name') == 'Test CLI artifact publication')
        self.assertNotIn('if', native)
        self.assertIn("test_notifier*.py", native['run'])
        verify = next(s for s in jobs['publish']['steps'] if s.get('name') == 'Authenticate the prepared build from this run')
        self.assertIn('--verify-local', verify['run'])
        self.assertIn('source-sha.txt', verify['run'])
        names = [s.get('name') for s in jobs['publish']['steps']]
        self.assertLess(names.index(verify['name']), names.index('Publish CLI binaries'))

    def test_cli_failure_stops_the_skills_gate(self):
        gate = workflow('release-skills.yml')['jobs']['cli_ready']['steps'][0]
        with tempfile.TemporaryDirectory() as directory:
            gh = Path(directory) / 'gh'
            gh.write_text('#!/bin/sh\nexit "$FIXTURE_EXIT"\n')
            gh.chmod(0o755)
            # A failed gate may be rerun after the upstream CLI is repaired.
            for exit_code in (1, 2, 0):
                env = dict(os.environ, PATH=directory + os.pathsep + os.environ['PATH'],
                           CLI_RUN_ID='123', FIXTURE_EXIT=str(exit_code))
                result = subprocess.run(['bash', '-e', '-c', gate['run']], env=env)
                self.assertEqual(result.returncode, exit_code)

    def test_ci_is_checked_after_unconditional_native_tests(self):
        steps = workflow('release-cli.yml')['jobs']['prepare']['steps']
        names = [s.get('name') for s in steps]
        self.assertLess(names.index('Test CLI artifact publication'), names.index('Recheck CI before uncovered tests'))
        fallback = next(s for s in steps if s.get('name') == 'Run tests not covered by successful main CI')
        self.assertEqual(fallback['if'], "steps.ci.outputs.trusted_ci != 'true'")
        self.assertIn('./tests ./internal/selfupdate', fallback['run'])

    def test_private_key_permissions_do_not_change_staged_tar_permissions(self):
        jobs = workflow('release-skills.yml')['jobs']
        step = next(s for s in jobs['prepare']['steps'] if s.get('name') == 'Prepare and verify signed Skills without reserving a release')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            scripts = root / 'cli/scripts'
            scripts.mkdir(parents=True)
            (scripts / 'release-skills.sh').write_text('mkdir -p build/cli/skills-stage; echo content > build/cli/skills-stage/file\n')
            env = dict(os.environ, RUNNER_TEMP=directory, EIGENFLUX_SKILLS_SIGNING_KEY='fixture')
            subprocess.run(['bash', '-e', '-c', 'umask 022\n' + step['run']], cwd=root, env=env, check=True)
            self.assertEqual(stat.S_IMODE((root / 'eigenflux-skills-signing-key').stat().st_mode), 0o600)
            self.assertEqual(stat.S_IMODE((root / 'build/cli/skills-stage/file').stat().st_mode), 0o644)

    def test_skills_freezes_one_source_and_defers_combined_releases(self):
        data = workflow('release-skills.yml')
        jobs = data['jobs']
        self.assertIn('defer_to_cli', jobs['prepare']['if'])
        for name in ('prepare', 'publish'):
            checkout = next(s for s in jobs[name]['steps'] if 'checkout@' in s.get('uses', ''))
            self.assertEqual(checkout['with']['ref'], '${{ needs.plan.outputs.source_sha }}')
        fallback = next(s for s in jobs['prepare']['steps'] if 'trusted_ci' in s.get('if', ''))
        self.assertIn("!= 'true'", fallback['if'])
        triggers = data.get('on', data.get('true'))
        self.assertNotIn('tests/cli_release/**', triggers['push']['paths'])
        self.assertIn('Release CLI', triggers['workflow_run']['workflows'])
        self.assertEqual(triggers['workflow_run']['types'], ['in_progress'])
        self.assertIn('cli_ready', jobs['publish']['needs'])
        self.assertNotIn('concurrency', jobs['cli_ready'])
        self.assertEqual(jobs['cli_ready']['needs'], ['plan', 'prepare'])
        gate = jobs['cli_ready']['steps'][0]
        self.assertIn('--exit-status', gate['run'])
        self.assertEqual(gate['env']['CLI_RUN_ID'], '${{ github.event.workflow_run.id }}')
        self.assertEqual(jobs['plan']['steps'][0]['with']['ref'], 'main')
        self.assertEqual(fallback['if'], "steps.ci.outputs.trusted_ci != 'true'")


    def test_duplicate_gate_precedes_all_preparation_and_records_only_success(self):
        jobs = workflow('release-skills.yml')['jobs']
        self.assertIn("needs.plan.outputs.duplicate_release != 'true'", jobs['prepare']['if'])
        decision = next(s for s in jobs['plan']['steps'] if s.get('id') == 'plan')
        self.assertIn('--check-duplicate', decision['run'])
        self.assertNotIn('SKILLS_SIGNING_KEY', json.dumps(jobs['plan']))
        self.assertIn('duplicate_release', jobs['plan']['outputs'])
        receipt = jobs['publish']['steps'][-1]
        self.assertIn('upload-artifact@', receipt['uses'])
        self.assertNotIn('if', receipt)
        self.assertEqual(receipt['with']['path'], 'build/cli/manifest.json')
        self.assertEqual(receipt['with']['name'], '${{ needs.plan.outputs.publication_receipt }}')
        self.assertGreaterEqual(receipt['with']['retention-days'], 14)
        triggers = workflow('release-skills.yml')
        triggers = triggers.get('on', triggers.get('true'))
        self.assertFalse(triggers['workflow_dispatch']['inputs']['force']['default'])
