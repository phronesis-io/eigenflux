"""Coverage routing must preserve unknown tests and fail closed on unknown diffs."""
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / 'scripts/ci' / (name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


changes = load('changes')
commands = load('cmd_tests')


class ScopeTests(unittest.TestCase):
    def test_docs_skills_full_and_mixed(self):
        self.assertEqual(changes.classify(['skills/AGENTS.md', 'docs/dev/testing.md']), {'full': False, 'skills': False})
        for paths in (['skills/ef-onboarding/references/prefill.md'], ['static/feed_contract.md'], ['tests/onboarding_prefill/cases.json']):
            self.assertEqual(changes.classify(paths), {'full': False, 'skills': True})
        for path in ('cli/internal/dispatch/service.go', 'cli/cmd/new_test.go', 'cli/go.mod', '.github/workflows/cli-auth-cross-platform.yml', 'scripts/ci/cmd_tests.py', 'skills/ef-new/scripts/runtime.py', 'docs/tools/check.py', 'unknown/path'):
            self.assertTrue(changes.classify([path, 'skills/AGENTS.md'])['full'])
        self.assertEqual(changes.classify(['skills/ef-profile/SKILL.md', 'cli/cmd/root.go']), {'full': True, 'skills': True})

    def test_unknown_base_or_manual_runs_do_not_skip_platform_checks(self):
        with patch.object(changes.subprocess, 'check_output') as git:
            for event, base in [('workflow_dispatch', 'a' * 40), ('push', '0' * 40), ('pull_request', '--flag')]:
                self.assertIsNone(changes.changed_paths(event, base, 'b' * 40))
            git.assert_not_called()
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / 'outputs'
            with patch('sys.argv', ['changes.py', '--event', 'workflow_dispatch', '--github-output', str(output)]):
                changes.main()
            self.assertIn('full=true', output.read_text())

    def test_diff_preserves_large_prs_and_removed_names(self):
        names = ['docs/%d.md' % i for i in range(350)] + ['cli/cmd/deleted.go']
        with patch.object(changes.subprocess, 'check_output', return_value=('\0'.join(names) + '\0').encode()) as git:
            actual = changes.changed_paths('pull_request', 'a' * 40, 'b' * 40)
            self.assertTrue(changes.classify(actual)['full'])
            self.assertEqual(len(actual), 351)
            self.assertIn('--no-renames', git.call_args.args[0])
            self.assertIn('a' * 40 + '...' + 'b' * 40, git.call_args.args[0])

    def test_real_git_rename_to_code_requires_full_checks(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            def git(*args):
                return subprocess.check_output(['git', '-c', 'user.name=CI Fixture', '-c',
                    'user.email=ci@example.invalid', *args], cwd=root, stderr=subprocess.DEVNULL).decode().strip()
            git('init')
            (root / 'docs').mkdir()
            (root / 'docs/input.md').write_text('fixture\n')
            git('add', '.')
            git('commit', '-m', 'base')
            base = git('rev-parse', 'HEAD')
            (root / 'cli/cmd').mkdir(parents=True)
            git('mv', 'docs/input.md', 'cli/cmd/runtime.go')
            git('commit', '-m', 'rename')
            head = git('rev-parse', 'HEAD')
            with patch.object(changes, 'ROOT', root):
                for event in ('pull_request', 'push'):
                    paths = changes.changed_paths(event, base, head)
                    self.assertEqual(set(paths), {'docs/input.md', 'cli/cmd/runtime.go'})
                    self.assertTrue(changes.classify(paths)['full'])


class CommandPartitionTests(unittest.TestCase):
    def test_new_or_changed_tests_default_to_platform(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = root / 'sample.go'
            body = b'func TestReviewed(t *testing.T) {}\n'
            source.write_bytes(body)
            manifest = root / 'manifest.json'
            manifest.write_text(json.dumps({'sample.go': {'sha256': hashlib.sha256(body).hexdigest(), 'tests': ['TestReviewed']}}))
            self.assertEqual(commands.portable_tests(root, manifest), ['TestReviewed'])
            # CRLF conversion does not invalidate review, but any content edit does.
            source.write_bytes(body.replace(b'\n', b'\r\n'))
            self.assertEqual(commands.portable_tests(root, manifest), ['TestReviewed'])
            source.write_bytes(body + b'func TestNew(t *testing.T) {}\n')
            self.assertEqual(commands.portable_tests(root, manifest), [])
            self.assertEqual(commands.test_command('platform', []), ['go', 'test', '-count=1', './cmd', '-skip', '^$'])
            with self.assertRaises(ValueError):
                commands.test_command('portable', [])

    def test_manifest_partitions_reviewed_and_unclassified_tests(self):
        # Do not compile Go in the lightweight source job. The integration
        # acceptance compares this partition with go test -list output.
        portable = commands.portable_tests()
        self.assertGreater(len(portable), 0)
        self.assertEqual(len(portable), len(set(portable)))
        import re
        portable_pattern = re.compile(commands.test_command('portable', portable)[-1])
        skipped_pattern = re.compile(commands.test_command('platform', portable)[-1])
        for name in portable + ['TestUnclassifiedNewPlatformBehavior']:
            groups = [bool(portable_pattern.fullmatch(name)), not bool(skipped_pattern.fullmatch(name))]
            self.assertEqual(sum(groups), 1)


class WorkflowRoutingTests(unittest.TestCase):
    def test_real_yaml_routes_lightweight_and_full_jobs(self):
        source = ROOT / '.github/workflows/cli-auth-cross-platform.yml'
        data = json.loads(subprocess.check_output([
            'ruby', '-ryaml', '-rjson', '-e',
            'puts JSON.generate(YAML.load_file(ARGV[0]))', str(source)], text=True))
        jobs = data['jobs']
        for name in ('auth-tests', 'windows-download-tests', 'release-artifact-handoff'):
            self.assertEqual(jobs[name]['needs'], 'scope')
            self.assertEqual(jobs[name]['if'], "needs.scope.outputs.full == 'true'")
        self.assertEqual(jobs['skills-tests']['if'], "needs.scope.outputs.full != 'true' && needs.scope.outputs.skills == 'true'")
        self.assertNotIn('setup-go@', json.dumps(jobs['source-checks']))
        steps = jobs['auth-tests']['steps']
        selected = [s for s in steps if 'cmd_tests.py' in s.get('run', '')]
        self.assertEqual(len(selected), 3)
        linux = next(s for s in selected if s['if'] == "runner.os == 'Linux'")
        self.assertTrue(linux['run'].endswith(' all'))
        for os_name in ('macOS', 'Windows'):
            platform = next(s for s in selected if s['if'] == f"runner.os == '{os_name}'")
            self.assertTrue(platform['run'].endswith(' platform'))


if __name__ == '__main__':
    unittest.main()
