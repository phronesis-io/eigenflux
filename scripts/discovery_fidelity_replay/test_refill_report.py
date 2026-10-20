import unittest
from refill_report import rates, expected, age_bucket, delivered_identities

class RefillReportTest(unittest.TestCase):
    def test_friend_provenance_survives_context_reattribution(self):
        data = {"Steps": [{"Delivered": [{
            "context": {"input_origin": "need_input"},
            "document": {"source_ref": {"type": "broadcast", "id": "10"},
                         "channels": ["lexical", "friend"]}}]}]}
        items, friends = delivered_identities(data)
        self.assertEqual(items, {("broadcast", "10")})
        self.assertEqual(friends, items)

    def test_missing_exposure_support_is_not_zero_quality(self):
        with self.assertRaises(ValueError):
            expected('1', {('ugc','over30'): 2}, rates([]))

    def test_zero_delivery_agent_and_source_age_sensitivity(self):
        model = rates([
            {'agent_id':'1','lane':'ugc','age':'under7','deliveries':100,'positives':20},
            {'agent_id':'1','lane':'ugc','age':'over30','deliveries':100,'positives':2},
        ])
        self.assertEqual(expected('2', {}, model), (0.0, 0.0))
        source, age = expected('2', {('ugc','over30'): 10}, model)
        self.assertAlmostEqual(source, 1.1)
        self.assertAlmostEqual(age, .2)
        self.assertEqual(age_bucket(7), 'under7')
        self.assertEqual(age_bucket(30), '7to30')

class FullCohortReportTest(unittest.TestCase):
    def test_keeps_zero_agents_and_rejects_changed_snapshots(self):
        import csv
        import hashlib
        import json
        import subprocess
        import sys
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            cohort = [{'AgentID': n} for n in range(1, 226)]
            (root / 'cohort.json').write_text(json.dumps(cohort))
            (root / 'rate-history.jsonl').write_text('')
            hashes = []
            for c in cohort:
                aid = c['AgentID']
                snapshot = {'CaptureError': '', 'At': 1000, 'CaptureMS': 10,
                            'Channels': {}, 'Classes': {}, 'Hydrated': {}, 'Seen': {}}
                raw = json.dumps(snapshot).encode()
                (root / f'refill-agent-{aid}.json').write_bytes(raw)
                hashes.append(hashlib.sha256(raw).hexdigest())
                steps = [{'Step': n, 'Delivered': [], 'Refill': None,
                          'PartialReasons': [], 'Rejections': {}} for n in range(1, 25)]
                before = {'Steps': steps, 'PoolIncomplete': False, 'PoolIncompleteReasons': None, 'Rejections': {}}
                after = dict(before, PoolIncomplete=aid == 2, PoolIncompleteReasons=['capture_depth'] if aid == 2 else None)
                (root / f'refill-comparison-{aid}.json').write_text(json.dumps(
                    {'Owner': aid, 'Before': before, 'After': after}))
            manifest = {'aa_equal': 225, 'input_sha256': {
                n: hashlib.sha256((root / n).read_bytes()).hexdigest()
                for n in ('cohort.json', 'rate-history.jsonl')},
                'snapshots_sha256': hashlib.sha256('\n'.join(hashes).encode()).hexdigest()}
            (root / 'refill-manifest.json').write_text(json.dumps(manifest))
            command = [sys.executable, str(Path(__file__).with_name('refill_report.py')),
                       '--dir', str(root), '--out', str(root / 'report')]
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            with (root / 'report/fixed-225-effects.csv').open() as file:
                rows = list(csv.DictReader(file))
            self.assertEqual(len(rows), 225)
            summary = json.loads((root / 'report/fixed-225-summary.json').read_text())
            self.assertEqual(summary['totals']['after']['zero_delivery_agents'], 225)
            self.assertEqual(summary['uncensored_agents'], 224)
            self.assertEqual(summary['censored_agent_reasons']['after']['capture_depth'], 1)
            self.assertEqual(summary['totals']['after']['censored_agents'], 1)
            (root / 'refill-agent-1.json').write_bytes(raw + b'\n')
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('snapshots changed', result.stderr)
