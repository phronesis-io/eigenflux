import unittest
from recommendation_aa import EXPERIMENT, arm, srm_p_value, summarize


def row(agent_id, observation_id, outcome="empty", count=0):
    return dict(experiment_id=EXPERIMENT, agent_id=str(agent_id), arm=arm(str(agent_id)), observation_id=observation_id, outcome=outcome, item_count=count, endpoint="feed_v2", caller="cli", pipeline_version="need_search_v1", impression_id="")


class RecommendationAATest(unittest.TestCase):
    def test_shared_go_allocation_contract(self):
        self.assertEqual([arm(str(x)) for x in (1, 2, 365412762643333120)], ["a2", "a2", "a1"])

    def test_repeated_requests_do_not_inflate_enrollment(self):
        rows = [row(1, "one"), row(1, "two"), row(365412762643333120, "three")]
        report = summarize(rows + [rows[0]], 3)
        self.assertEqual(report["requests"], 3)
        self.assertEqual(report["arms"]["a2"]["unique_agents"], 1)
        self.assertEqual(report["unique_agent_srm_p"], 1)
        self.assertEqual(report["duplicate_export_rows"], 1)
        self.assertEqual(report["status"], "observation_only")

    def test_all_failed_requests_are_enrolled(self):
        report = summarize([row(1, "failed", "http_error", None)], 1)
        self.assertEqual(report["arms"]["a2"]["agent_weighted_error_rate"], 1)
        self.assertNotIn("no_enrolled_agents", report["measurement_issues"])

    def test_incomplete_and_invalid_exports_are_never_accepted(self):
        self.assertIn("coverage_unverified", summarize([row(1, "one")])["measurement_issues"])
        self.assertIn("completion_counter_export_mismatch", summarize([row(1, "one")], 2)["measurement_issues"])
        o = row(1, "one"); o["arm"] = "a1"
        with self.assertRaises(ValueError): summarize([o], 1)
        with self.assertRaises(ValueError): summarize([row(1,"one",count=None)], 1)
        self.assertIn("nonempty_without_impression_id", summarize([row(1,"one","nonempty",1)],1)["measurement_issues"])

    def test_in_progress_is_separate_from_service_failure(self):
        report = summarize([row(1,"busy","in_progress",None)],1)
        self.assertEqual(report["arms"]["a2"]["agent_weighted_error_rate"],0)
        self.assertEqual(report["arms"]["a2"]["agent_weighted_in_progress_rate"],1)

    def test_exact_binomial_tail(self):
        self.assertAlmostEqual(srm_p_value(0, 10), 2 / 1024)
        self.assertEqual(srm_p_value(50, 50), 1)
        self.assertIsNone(srm_p_value(0, 0))
        self.assertLess(srm_p_value(40, 100), .001)


if __name__ == "__main__": unittest.main()
