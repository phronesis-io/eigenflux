import copy
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import run


class RegressionCheckerTest(unittest.TestCase):
    def setUp(self):
        self.cases = json.loads(run.CASES.read_text())
        self.base = {"identity_card": {"agent_description": "中文文案与文档制作", "offering": ["文档服务"]}, "intent_actions": []}
        self.positive = copy.deepcopy(self.base)
        self.positive["intent_actions"] = [{"watch_for": "真实付费委托需求", "trigger_when": "与服务匹配", "action_instruction": "整理给用户确认后再联系", "action_policy": "analyze_only"}]

    def test_pair_has_only_goal_addition(self):
        self.assertTrue(self.cases[1]["context"].startswith(self.cases[0]["context"]))
        self.assertFalse(self.cases[0]["expects_commission_intent"])
        self.assertTrue(self.cases[1]["expects_commission_intent"])

    def test_behavioral_failures_are_rejected(self):
        self.assertEqual(run.validate(self.cases[0], self.base), 0)
        self.assertEqual(run.validate(self.cases[1], self.positive), 1)
        for case, draft in [(self.cases[0], self.positive), (self.cases[1], self.base), (self.cases[0], [])]:
            with self.subTest(case=case["id"], draft=draft), self.assertRaises(ValueError):
                run.validate(case, draft)
        for key, value in [("action_policy", "network_action"), ("watch_for", ""), ("trigger_when", None)]:
            draft = copy.deepcopy(self.positive)
            draft["intent_actions"][0][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                run.validate(self.cases[1], draft)

    def test_stale_run_and_missing_results_fail(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "run"
            run.prepare(output)
            with self.assertRaises(FileNotFoundError):
                run.check(output)
            with patch.object(run, "fingerprint", return_value={"changed": "hash"}):
                with self.assertRaisesRegex(ValueError, "source or fixtures changed"):
                    run.check(output)


if __name__ == "__main__":
    unittest.main()
