import runpy
import subprocess
import unittest
from pathlib import Path
from unittest.mock import patch

check = runpy.run_path(str(Path(__file__).resolve().parents[2] / "cli/scripts/stapler-ticket.py"))["check"]


class TicketTests(unittest.TestCase):
    def test_transient_then_valid(self):
        lost = subprocess.CompletedProcess([], 68, "NSURLErrorDomain Code=-1005")
        valid = subprocess.CompletedProcess([], 0, "validated")
        with patch("subprocess.run", side_effect=[lost, valid]) as run, patch("time.sleep"):
            self.assertEqual(check("validate", "app"), 0)
            self.assertEqual(run.call_count, 2)

    def test_invalid_and_exhausted_fail_closed(self):
        for message, attempts in [("invalid ticket", 1), ("NSURLErrorDomain Code=-1005", 4)]:
            with patch("subprocess.run", return_value=subprocess.CompletedProcess([], 68, message)) as run, patch("time.sleep"):
                self.assertEqual(check("staple", "app"), 68)
                self.assertEqual(run.call_count, attempts)
