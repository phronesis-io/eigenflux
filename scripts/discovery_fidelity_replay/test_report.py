import unittest
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


if __name__ == "__main__":
    unittest.main()
