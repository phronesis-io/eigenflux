"""Exercise the changed-file gate with the same shell settings as Actions."""
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]


class WorkflowGateTest(unittest.TestCase):
    def gate(self, filenames, status=0):
        workflow = (ROOT / ".github/workflows/release-installer.yml").read_text()
        lines = workflow.split("        run: |\n", 1)[1].splitlines()
        body = []
        for line in lines:
            if line.strip() and not line.startswith("          "):
                break
            body.append(line)
        script = textwrap.dedent("\n".join(body))
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            gh = directory / "gh"
            gh.write_text('#!/bin/sh\nprintf "%s\\n" "$TEST_FILES"\nexit "$TEST_STATUS"\n')
            gh.chmod(0o755)
            output = directory / "output"
            output.touch()
            env = dict(os.environ, PATH=f"{temp}:/usr/bin:/bin", RUNNER_TEMP=temp,
                       GITHUB_OUTPUT=str(output), GH_REPO="phronesis-io/eigenflux",
                       PR_NUMBER="123", TEST_FILES=filenames, TEST_STATUS=str(status))
            result = subprocess.run(["bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script],
                                    env=env, capture_output=True, text=True)
            return result.returncode, output.read_text()

    def test_script_change_publishes_even_after_many_other_files(self):
        files = "\n".join(f"other/file-{i}" for i in range(350)) + "\nstatic/install.sh"
        self.assertEqual(self.gate(files), (0, "installer=true\n"))

    def test_unrelated_changes_do_not_publish(self):
        self.assertEqual(self.gate("skills/install.md\nstatic/install.ps1\nstatic/install.sh.backup"), (0, ""))

    def test_github_failure_does_not_publish(self):
        code, output = self.gate("static/install.sh", 1)
        self.assertNotEqual(code, 0)
        self.assertEqual(output, "")

    def test_only_changed_release_jobs_enter_concurrency(self):
        workflow = (ROOT / ".github/workflows/release-installer.yml").read_text()
        self.assertNotRegex(workflow, r"(?m)^concurrency:")
        gate, release = workflow.split("  release-installer:\n", 1)
        self.assertIn("  check-installer:\n", gate)
        self.assertIn("if: github.event.pull_request.merged == true", gate)
        self.assertNotIn("concurrency:", gate)
        self.assertIn("installer: ${{ steps.changed.outputs.installer }}", gate)
        self.assertIn("    needs: check-installer\n", release)
        self.assertIn("    if: needs.check-installer.outputs.installer == 'true'\n", release)
        self.assertIn("    concurrency:\n      group: release-installer\n      cancel-in-progress: false", release)
