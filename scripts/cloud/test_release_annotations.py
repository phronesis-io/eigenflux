import copy
from datetime import datetime, timezone
import importlib.util
import json
import multiprocessing
from pathlib import Path
import tempfile
import unittest
from urllib.parse import parse_qs, urlsplit

spec = importlib.util.spec_from_file_location('release_annotations', Path(__file__).with_name('release_annotations.py'))
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)


def event(event_id='instrument-check', scope='api'):
    return dict(id=event_id, kind='instrumentation', deploy_scope=scope,
                dashboards=['card-refresh'], title='检查完成开始记录',
                description='旧客户端可能不完整；人工检查不算',
                pr='https://github.com/phronesis-io/eigenflux/pull/400')


class MemoryGrafana:
    def __init__(self):
        self.rows = []
        self.fail = None
        self.posts = 0
        self.during_post = None

    def request(self, method, path, body=None):
        if self.fail == 'down':
            raise RuntimeError('unavailable')
        if method == 'GET':
            tag = parse_qs(urlsplit(path).query)['tags'][0]
            return [copy.deepcopy(row) for row in self.rows if tag in row['tags']]
        assert method == 'POST'
        self.posts += 1
        self.rows.append(copy.deepcopy(body))
        if self.during_post:
            self.during_post()
        if self.fail == 'lost':
            raise RuntimeError('write succeeded but response lost')
        return {}


def competing_record(path):
    r.record(Path(path), [event()], 'api', 'a'*40)


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name)
        self.client = MemoryGrafana()

    def test_manifest_validation(self):
        f = self.path / 'manifest.json'
        self.assertEqual(r.load_manifest(f), [])
        for update in [dict(id='../x'), dict(kind='other'), dict(deploy_scope='rpc'),
                       dict(dashboards=['growth']), dict(title=''), dict(pr='https://bad.invalid')]:
            f.write_text(json.dumps([dict(event(), **update)]))
            with self.assertRaises(ValueError): r.load_manifest(f)
        f.write_text(json.dumps([event()]))
        self.assertEqual(r.load_manifest(f), [event()])
        growth_event = dict(event(), dashboards=['user-growth'])
        f.write_text(json.dumps([growth_event]))
        self.assertEqual(r.load_manifest(f), [growth_event])
        f.write_text(json.dumps([event(), event()]))
        with self.assertRaises(ValueError): r.load_manifest(f)

    def test_compact_payload_preserves_provenance_and_escapes_copy(self):
        moment = datetime(2026, 10, 13, 6, 1, 23, tzinfo=timezone.utc)
        for kind, label in [('instrumentation', '埋点'), ('behavior', '功能')]:
            e = dict(event(), kind=kind, title='<title>', description='A & B')
            payload = r.make_payload(e, 'test-id', 'activate', 'a'*40, moment)
            self.assertIn(label + '｜&lt;title&gt;', payload['text'])
            self.assertIn('A &amp; B', payload['text'])
            self.assertIn('2026-10-13 14:01（UTC+8）', payload['text'])
            self.assertIn(e['pr'], payload['text'])
            self.assertIn('deployed-commit:' + 'a'*40, payload['tags'])
            self.assertEqual(payload['time'], int(moment.timestamp()*1000))
            self.assertEqual(payload['timeEnd'], payload['time'])

    def test_api_only_never_marks_full_rollout_and_retries_do_not_duplicate(self):
        events = [event(), event('sort-change', 'full')]
        self.assertEqual(r.record(self.path, events, 'api', 'a'*40), 1)
        self.assertEqual(r.record(self.path, events, 'api', 'a'*40), 0)
        self.assertEqual(r.record(self.path, events, 'full', 'b'*40), 1)
        self.assertEqual(r.flush(self.path, self.client), 2)
        self.assertEqual(r.flush(self.path, self.client), 0)
        # API-only rollback must leave the full-service declaration active.
        self.assertEqual(r.record(self.path, [], 'api', 'c'*40), 1)
        self.assertEqual(set(r.load_state(self.path)['active']), {'sort-change'})

    def test_rollback_and_reactivation_preserve_history_and_real_time(self):
        moment = datetime(2026, 10, 13, 6, 1, tzinfo=timezone.utc)
        r.record(self.path, [event()], 'full', 'a'*40, moment)
        r.record(self.path, [], 'full', 'b'*40, moment)
        r.record(self.path, [event()], 'full', 'a'*40, moment)
        state = r.load_state(self.path)
        self.assertEqual(len(state['pending']), 3)
        self.assertEqual(len(set(state['pending'])), 3)
        self.assertTrue(all(p['time'] == int(moment.timestamp()*1000) for p in state['pending'].values()))
        r.flush(self.path, self.client)
        self.assertEqual(len(self.client.rows), 3)
        self.assertIn('停止生效', self.client.rows[1]['text'])

    def test_outage_and_accepted_but_lost_response_are_reconciled(self):
        r.record(self.path, [event()], 'api', 'a'*40)
        original = copy.deepcopy(r.load_state(self.path)['pending'])
        for failure in ('down', 'lost'):
            self.client.fail = failure
            with self.assertRaises(RuntimeError): r.flush(self.path, self.client)
            self.assertEqual(r.load_state(self.path)['pending'], original)
        self.client.fail = None
        r.flush(self.path, self.client)
        self.assertEqual(self.client.posts, 1)
        self.assertEqual(r.load_state(self.path)['pending'], {})

    def test_concurrent_recorders_and_writer_isolation(self):
        ctx = multiprocessing.get_context('fork')
        workers = [ctx.Process(target=competing_record, args=(str(self.path),)) for _ in range(4)]
        for w in workers: w.start()
        for w in workers:
            w.join(5)
            self.assertEqual(w.exitcode, 0)
        self.assertEqual(len(r.load_state(self.path)['pending']), 1)
        # Recording another release while Grafana is responding does not block
        # on the writer lock, nor get lost when the first event is acknowledged.
        self.client.during_post = lambda: r.record(self.path, [event(), event('second')], 'api', 'b'*40)
        r.flush(self.path, self.client)
        self.assertEqual(len(r.load_state(self.path)['pending']), 1)
        with r.locked(self.path, 'writer.lock'):
            with self.assertRaises(BlockingIOError): r.flush(self.path, self.client)

    def test_crash_or_timeout_before_post_observation_never_blindly_reposts(self):
        r.record(self.path, [event()], 'api', 'a'*40)
        state = r.load_state(self.path)
        state['attempted'] = list(state['pending'])
        r.save_state(self.path, state)
        with self.assertRaisesRegex(RuntimeError, 'outcome unknown'):
            r.flush(self.path, self.client)
        self.assertEqual(self.client.posts, 0)
        self.assertEqual(len(r.load_state(self.path)['pending']), 1)

    def test_changed_or_foreign_identity_requires_review(self):
        r.record(self.path, [event()], 'api', 'a'*40)
        with self.assertRaises(ValueError):
            r.record(self.path, [dict(event(), title='changed')], 'api', 'b'*40)
        wanted = next(iter(r.load_state(self.path)['pending'].values()))
        for rows in ([dict(wanted, panelId=1)], [wanted, wanted], [dict(wanted, text='changed')]):
            self.client.rows = rows
            with self.assertRaises(RuntimeError): r.flush(self.path, self.client)
            self.assertEqual(self.client.posts, 0)
            self.assertEqual(len(r.load_state(self.path)['pending']), 1)


if __name__ == '__main__': unittest.main()
