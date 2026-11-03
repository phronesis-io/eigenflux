"""Release decisions fail closed on missing or unrelated CI evidence."""
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('release_plan', ROOT / 'cli/scripts/release-plan.py')
plan = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plan)
REPO = 'phronesis-io/eigenflux'
HEAD = 'a' * 40
BEFORE = 'b' * 40


def run_fixture(**changes):
    run = {'id': 123, 'name': plan.WORKFLOW_NAME, 'path': plan.WORKFLOW,
           'head_sha': HEAD, 'event': 'push', 'head_branch': 'main',
           'head_repository': {'full_name': REPO}, 'repository': {'full_name': REPO},
           'status': 'completed', 'conclusion': 'success'}
    run.update(changes)
    return run


class ReleasePlanTests(unittest.TestCase):
    def test_ci_requires_exact_trusted_main_push_workflow(self):
        self.assertEqual(plan.ci_evidence({'workflow_runs': [run_fixture()]}, REPO, HEAD),
                         (True, '123', 'ci_verified'))
        for change in ({'head_sha': BEFORE}, {'event': 'pull_request'},
                       {'head_branch': 'feature'}, {'name': 'Other workflow'},
                       {'path': '.github/workflows/untrusted.yml'},
                       {'head_repository': {'full_name': 'fork/eigenflux'}},
                       {'head_repository': None}, {'repository': {'full_name': 'fork/eigenflux'}},
                       {'status': 'in_progress'}, {'conclusion': 'failure'},
                       {'conclusion': 'cancelled'}, {'id': '123\ninjected=true'}):
            with self.subTest(change=change):
                self.assertFalse(plan.ci_evidence({'workflow_runs': [run_fixture(**change)]}, REPO, HEAD)[0])

    def test_new_pending_or_failed_run_does_not_reuse_old_success(self):
        for change in ({'status': 'in_progress'}, {'conclusion': 'failure'}):
            self.assertFalse(plan.ci_evidence({'workflow_runs': [run_fixture(**change), run_fixture()]}, REPO, HEAD)[0])
        for data in ({}, {'workflow_runs': []}, {'workflow_runs': None}, {'workflow_runs': ['invalid']}):
            self.assertFalse(plan.ci_evidence(data, REPO, HEAD)[0])

    def test_api_errors_and_invalid_json_require_full_tests(self):
        for failure in (OSError('offline'), subprocess.TimeoutExpired('gh', 30),
                        subprocess.CalledProcessError(1, 'gh')):
            with self.subTest(failure=type(failure).__name__), patch.object(plan.subprocess, 'run', side_effect=failure):
                self.assertEqual(plan.trusted_ci(REPO, HEAD), (False, '', 'ci_unavailable'))
        with patch.object(plan.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '<html>', '')):
            self.assertFalse(plan.trusted_ci(REPO, HEAD)[0])
        with patch.object(plan.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, json.dumps({'workflow_runs': [run_fixture()]}), '')) as command:
            self.assertTrue(plan.trusted_ci(REPO, HEAD)[0])
            args = command.call_args.args[0]
            self.assertIn(f'head_sha={HEAD}', args)
            self.assertIn('event=push', args)
            self.assertIn('branch=main', args)
            self.assertIn('per_page=1', args)
            self.assertIn('cli-auth-cross-platform.yml/runs', args[2])
        with patch.object(plan.subprocess, 'run') as command:
            self.assertFalse(plan.trusted_ci('../untrusted', HEAD)[0])
            self.assertFalse(plan.trusted_ci(REPO, '--flag')[0])
            command.assert_not_called()

    def test_cli_bump_defers_skills_but_skills_only_change_does_not(self):
        event = {'ref': 'refs/heads/main', 'after': HEAD, 'before': BEFORE,
                 'repository': {'full_name': REPO}}
        for versions, deferred in ((['1.0.5', '1.0.6'], True), (['1.0.5', '1.0.5'], False)):
            with patch.object(plan, 'cli_version_at', side_effect=versions) as version:
                self.assertEqual(plan.defer_skills('push', event, REPO, HEAD)[0], deferred)
                self.assertEqual([c.args[0] for c in version.call_args_list], [BEFORE, HEAD])
        for failure in (KeyError('before'), ValueError('bad config'), subprocess.CalledProcessError(128, 'git')):
            with patch.object(plan, 'cli_version_at', side_effect=failure):
                self.assertTrue(plan.defer_skills('push', event, REPO, HEAD)[0])
        for invalid in ({}, dict(event, ref='refs/heads/other'), dict(event, after='invalid'),
                        dict(event, repository={'full_name': 'fork/eigenflux'})):
            self.assertTrue(plan.defer_skills('push', invalid, REPO, HEAD)[0])
        with patch.object(plan, 'cli_version_at') as version:
            self.assertTrue(plan.defer_skills('workflow_run', {}, REPO, HEAD)[0])
            self.assertFalse(plan.defer_skills('workflow_dispatch', {}, REPO, HEAD)[0])
            version.assert_not_called()

    def test_queued_skills_push_uses_descendant_main_without_losing_update(self):
        after = 'c' * 40
        event = {'ref': 'refs/heads/main', 'after': after, 'before': BEFORE,
                 'repository': {'full_name': REPO}}
        for versions, deferred in ((['1.0.5', '1.0.5'], False), (['1.0.5', '1.0.6'], True)):
            with patch.object(plan.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '', '')) as command, patch.object(plan, 'cli_version_at', side_effect=versions) as version:
                self.assertEqual(plan.defer_skills('push', event, REPO, HEAD)[0], deferred)
                self.assertEqual(command.call_args.args[0],
                                 ['git', '-C', str(plan.ROOT), 'merge-base', '--is-ancestor', after, HEAD])
                self.assertTrue(command.call_args.kwargs['check'])
                self.assertEqual([call.args[0] for call in version.call_args_list], [BEFORE, HEAD])
        for failure in (subprocess.CalledProcessError(1, 'git'),
                        subprocess.CalledProcessError(128, 'git'),
                        subprocess.TimeoutExpired('git', 10)):
            with self.subTest(failure=type(failure).__name__), patch.object(plan.subprocess, 'run', side_effect=failure), patch.object(plan, 'cli_version_at') as version:
                self.assertTrue(plan.defer_skills('push', event, REPO, HEAD)[0])
                version.assert_not_called()
        for invalid_after in ('invalid', '--flag', None):
            with patch.object(plan.subprocess, 'run') as command, patch.object(plan, 'cli_version_at') as version:
                self.assertTrue(plan.defer_skills('push', dict(event, after=invalid_after), REPO, HEAD)[0])
                command.assert_not_called()
                version.assert_not_called()

    def test_old_cli_success_cannot_publish_skills_for_new_cli_version(self):
        event = {'workflow_run': {'conclusion': 'success', 'head_branch': 'main',
                                 'name': 'Release CLI', 'head_sha': BEFORE,
                                 'head_repository': {'full_name': REPO}}}
        for versions, deferred in ((['1.0.5', '1.0.6'], True), (['1.0.5', '1.0.5'], False)):
            with patch.object(plan, 'cli_version_at', side_effect=versions) as version:
                self.assertEqual(plan.defer_skills('workflow_run', event, REPO, HEAD)[0], deferred)
                self.assertEqual([c.args[0] for c in version.call_args_list], [BEFORE, HEAD])
        with patch.object(plan, 'cli_version_at', side_effect=ValueError('missing upstream commit')):
            self.assertTrue(plan.defer_skills('workflow_run', event, REPO, HEAD)[0])
        for change in ({'conclusion': 'failure'}, {'head_branch': 'feature'},
                       {'name': 'Untrusted workflow'}, {'head_repository': {'full_name': 'fork/eigenflux'}}):
            with patch.object(plan, 'cli_version_at') as version:
                self.assertTrue(plan.defer_skills('workflow_run', {'workflow_run': dict(event['workflow_run'], **change)}, REPO, HEAD)[0])
                version.assert_not_called()

    def test_in_progress_cli_allows_preparation_only_for_matching_version(self):
        run = {'status': 'in_progress', 'conclusion': None, 'head_branch': 'main',
               'name': 'Release CLI', 'head_sha': BEFORE,
               'path': '.github/workflows/release-cli.yml',
               'head_repository': {'full_name': REPO}}
        event = {'action': 'in_progress', 'workflow_run': run}
        with patch.object(plan, 'cli_version_at', side_effect=['1.0.6', '1.0.6']):
            self.assertFalse(plan.defer_skills('workflow_run', event, REPO, HEAD)[0])
        with patch.object(plan, 'cli_version_at', side_effect=['1.0.6', '1.0.7']):
            self.assertTrue(plan.defer_skills('workflow_run', event, REPO, HEAD)[0])
        for change in ({'status': 'completed', 'conclusion': 'failure'},
                       {'path': '.github/workflows/untrusted.yml'},
                       {'head_repository': {'full_name': 'fork/eigenflux'}}):
            with patch.object(plan, 'cli_version_at') as version:
                self.assertTrue(plan.defer_skills('workflow_run', dict(event, workflow_run=dict(run, **change)), REPO, HEAD)[0])
                version.assert_not_called()

    def test_config_is_read_from_pinned_commits_and_validated(self):
        with patch.object(plan.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, 'CLI_VERSION=1.0.5\nNOTIFIER_VERSION=1.0.4\n', '')) as command:
            self.assertEqual(plan.cli_version_at(HEAD), '1.0.5')
            self.assertIn(HEAD + ':cli/.cli.config', command.call_args.args[0])
        for text in ('CLI_VERSION=01.0.5\n', 'CLI_VERSION=dev\n', 'CLI_VERSION=1.0.5\nCLI_VERSION=1.0.6\n'):
            with patch.object(plan.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, text, '')):
                with self.assertRaises(ValueError):
                    plan.cli_version_at(HEAD)
        with self.assertRaises(ValueError):
            plan.cli_version_at('0' * 40)

    def test_main_emits_only_bounded_github_outputs(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'github-output'
            with patch('sys.argv', ['release-plan.py', '--repository', REPO, '--sha', HEAD,
                                   '--event-name', 'push', '--event-path', str(Path(directory) / 'missing.json'),
                                   '--github-output', str(output)]), patch.object(plan, 'trusted_ci', return_value=(False, '', 'ci_unavailable')):
                plan.main()
            value = output.read_text()
            self.assertIn('trusted_ci=false\n', value)
            self.assertIn('defer_to_cli=true\n', value)
            self.assertIn('ci_run_id=\n', value)
