import unittest
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from report import estimate_counts


class PositiveEstimateTests(unittest.TestCase):
    def test_source_mix_uses_actual_positive_rates_not_followup_scores(self):
        priors = {"pgc": 0.1, "ugc": 0.4}
        history = {("a", "pgc"): 80, ("a", "ugc"): 80}
        observed = {("a", "pgc"): 8, ("a", "ugc"): 32}
        before = estimate_counts({"pgc": 10, "ugc": 10}, "a", history, observed, priors)
        after = estimate_counts({"pgc": 20}, "a", history, observed, priors)
        self.assertAlmostEqual(before, 5)
        self.assertAlmostEqual(after, 2)

    def test_sparse_agent_uses_explicit_source_prior(self):
        self.assertAlmostEqual(
            estimate_counts({"ugc": 10}, "new", {}, {}, {"ugc": 0.2}), 2
        )

    def test_slop_report_preserves_agent_and_delivery_basis(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            def save(name, value):
                (root / name).write_text(json.dumps(value))
            save("counterfactual-slop-manifest.json", {
                "method": "gate-only; frozen pools",
                "agents": [{"agent_id": "99", "pool": 2, "captured_delivered": 1}],
            })
            save("agent-99.json", {"Classes": {"1": "ugc", "2": "pgc"}})
            def arm(item, origin):
                return {"candidates": [{"document": {"source_ref": {"id": item}},
                                         "context": {"input_origin": origin}}]}
            save("comparison-slop-99.json", {"before": arm("1", "need_input"),
                                             "after": arm("2", "agent_context")})
            save("feedback-history.json", {"delivery_start": "start", "delivery_end": "end",
                "cells": [{"agent_id": "99", "lane": lane, "deliveries": 80}
                          for lane in ("pgc", "ugc")]})
            save("feedback-observed.json", {"cells": [
                {"agent_id": "99", "lane": "pgc", "positive_events": 8},
                {"agent_id": "99", "lane": "ugc", "positive_events": 32}]})
            subprocess.run([sys.executable, str(Path(__file__).with_name("report.py")),
                            "--dir", tmp, "--variant", "slop"], check=True, capture_output=True)
            report = json.loads((root / "slop-effect-estimates.json").read_text())
            row = report["rows"][0]
            self.assertEqual(row["historical_deliveries"], 160)
            self.assertEqual(row["before_need"], 1)
            self.assertEqual(row["after_profile"], 1)
            self.assertEqual(row["before_expected_positive_events"], .4)
            self.assertEqual(row["after_expected_positive_events"], .1)
            self.assertIn("gate-only", report["method"])


if __name__ == "__main__":
    unittest.main()
