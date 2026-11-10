"""Read-only grouping sensitivity for UGC whose extracted type changed.

The retained production corpus cannot reconstruct deleted historical neighbors.
Results are sensitivity evidence, not a reconstruction of publication-time state.
"""
import argparse
import base64
import concurrent.futures
import datetime
import json
import os
from pathlib import Path
import struct
import urllib.request


def f32(value):
    return struct.unpack('f', struct.pack('f', value))[0]


def group_for(item_id, author, kind, at_ms, hits):
    if not hits:
        return str(item_id)
    top = hits[0]
    source = top['source']
    keep = True
    if kind in ('demand', 'supply'):
        keep = int(source.get('author_agent_id', 0)) == int(author) and int(author) != 0
    if kind == 'alert':
        created = source['created_at']
        ms = datetime.datetime.fromisoformat(created.replace('Z', '+00:00')).timestamp() * 1000 if isinstance(created, str) else created
        keep = top['cosine'] >= 0.85 and ms > at_ms - 21600000
    return str(source['group_id']) if keep else str(item_id)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dir', type=Path, required=True)
    parser.add_argument('--arm', choices=('output', 'baseline'), default='output')
    args = parser.parse_args()
    root = args.dir
    inventory = {d['source_ref']['id']: d for f in root.glob('agent-*.json') for d in json.loads(f.read_text())['Inventory']}
    items = {x['id']: x for x in json.loads((root / 'items.json').read_text())}
    evaluations = json.loads((root / 'ownership-evaluations.json').read_text())
    if args.arm == 'baseline':
        evaluations = {k: dict(x, output=x.get('baseline'), blocked=x.get('baseline_blocked')) for k, x in evaluations.items()}
    output = root / ('ownership-baseline-neighbors.json' if args.arm == 'baseline' else 'ownership-neighbors.json')
    done = json.loads(output.read_text()) if output.exists() else {}
    auth = 'Basic ' + base64.b64encode((os.environ.get('ES_USERNAME', '') + ':' + os.environ.get('ES_PASSWORD', '')).encode()).decode()

    def search(query):
        request = urllib.request.Request(os.environ['ES_URL'].rstrip('/') + '/items-*/_search', data=json.dumps(query).encode(), headers={'Content-Type': 'application/json', 'Authorization': auth})
        with urllib.request.urlopen(request, timeout=30) as response:
            result = json.load(response)
        if result.get('timed_out') or result.get('_shards', {}).get('failed'):
            raise RuntimeError('partial ES search')
        return result['hits']['hits']

    def call(pair):
        item_id, evaluation = pair
        d, item = inventory[item_id], items[item_id]
        kind = evaluation['output']['broadcast_type']
        identity = {'input_hash': evaluation['input_hash'], 'source_version': d['version'], 'kind': kind, 'policy': 'ugc-grouping-v1-f32'}
        old = done.get(item_id, {})
        if all(old.get(k) == v for k, v in identity.items()) and old.get('group_id') and not old.get('error'):
            return item_id, old
        row = dict(identity)
        try:
            source_hits = search({'size': 2, 'query': {'term': {'id': int(item_id)}}, '_source': ['embedding', 'content', 'summary']})
            if len(source_hits) != 1:
                raise ValueError('missing/ambiguous frozen source')
            source = source_hits[0]['_source']
            if source.get('content', '') != item['content'] or source.get('summary', '') != item['summary']:
                raise ValueError('source drift')
            vector = source['embedding']
            if not vector:
                raise ValueError('missing original embedding')
            hits = search({'size': 5, 'knn': {'field': 'embedding', 'query_vector': vector, 'k': 5, 'num_candidates': 50, 'filter': {'bool': {'filter': [{'exists': {'field': 'group_id'}}, {'range': {'id': {'lt': int(item_id)}}}]}}}, '_source': ['id', 'group_id', 'author_agent_id', 'created_at', 'type']})
            neighbors = [{'source': h['_source'], 'cosine': f32(2 * h['_score'] - 1)} for h in hits if f32(2 * h['_score'] - 1) >= f32(0.8)]
            row['neighbors'] = neighbors
            row['group_id'] = group_for(item_id, d['author_id'], kind, d['fresh_at'], neighbors)
        except Exception as exc:
            row['error'] = str(exc) if isinstance(exc, ValueError) else type(exc).__name__
        return item_id, row

    pending = [(k, x) for k, x in evaluations.items() if k in inventory and x.get('output') and not x.get('error') and not x.get('blocked') and x['output']['broadcast_type'] != inventory[k].get('content_type')]
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        for n, (item_id, row) in enumerate(pool.map(call, pending), 1):
            done[item_id] = row
            temporary = output.with_suffix('.tmp')
            temporary.write_text(json.dumps(done))
            temporary.replace(output)
            print('UGC grouping', n, '/', len(pending), 'error', bool(row.get('error')), flush=True)
    if any(x.get('error') for x in done.values()):
        raise SystemExit('grouping failures remain')


if __name__ == '__main__':
    main()
