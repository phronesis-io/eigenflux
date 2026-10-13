import argparse
import base64
import concurrent.futures
import datetime
import hashlib
import json
import os
import pathlib
import struct
import urllib.request


def f32(x):
    return struct.unpack("f", struct.pack("f", x))[0]


parser = argparse.ArgumentParser(
    description="Read-only PGC grouping sensitivity against preceding retained ES items"
)
parser.add_argument("--dir", type=pathlib.Path, required=True)
args = parser.parse_args()
r = args.dir
env = os.environ
rows = {}
for line in (r / "pgc-core-paired.jsonl").read_text().splitlines():
    x = json.loads(line)
    if not x.get("error") and x.get("vector"):
        rows[x["sample"]["id"], x["sample"]["enabled"]] = x
outpath = r / "dedup-neighbors.jsonl"
done = {}
if outpath.exists():
    for line in outpath.read_text().splitlines():
        x = json.loads(line)
        if not x.get("error") and x.get("policy_version") == "grouping-v1-float32":
            done[x["id"], x["enabled"], x["vector_hash"]] = x
inventory = {
    d["source_ref"]["id"]: d
    for f in r.glob("agent-*.json")
    for d in json.loads(f.read_text())["Inventory"]
}


def call(x):
    s = x["sample"]
    id = s["id"]
    vec = x["vector"]
    vh = hashlib.sha256(json.dumps(vec).encode()).hexdigest()
    row = {
        "id": id,
        "enabled": s["enabled"],
        "vector_hash": vh,
        "policy_version": "grouping-v1-float32",
    }
    if (id, s["enabled"], vh) in done:
        return None
    query = {
        "size": 5,
        "knn": {
            "field": "embedding",
            "query_vector": vec,
            "k": 5,
            "num_candidates": 50,
            "filter": {
                "bool": {
                    "filter": [
                        {"exists": {"field": "group_id"}},
                        {"range": {"id": {"lt": int(id)}}},
                    ]
                }
            },
        },
        "_source": ["id", "group_id", "author_agent_id", "created_at", "type"],
    }
    try:
        req = urllib.request.Request(
            env["ES_URL"] + "/items-*/_search",
            data=json.dumps(query).encode(),
            headers={
                "Content-Type": "application/json",
                "Authorization": "Basic "
                + base64.b64encode(
                    (env["ES_USERNAME"] + ":" + env["ES_PASSWORD"]).encode()
                ).decode(),
            },
        )
        with urllib.request.urlopen(req, timeout=60) as response:
            data = json.load(response)
        hits = [
            {"source": h["_source"], "cosine": f32(2 * h["_score"] - 1)}
            for h in data["hits"]["hits"]
            if f32(2 * h["_score"] - 1) >= f32(0.8)
        ]
        row["neighbors"] = hits
        group = int(id)
        if hits:
            h = hits[0]
            src = h["source"]
            kind = x["output"]["broadcast_type"]
            keep = True
            if kind in ("demand", "supply"):
                keep = str(src.get("author_agent_id")) == inventory[id]["author_id"]
            if kind == "alert":
                created = src.get("created_at")
                ms = (
                    datetime.datetime.fromisoformat(
                        created.replace("Z", "+00:00")
                    ).timestamp()
                    * 1000
                    if isinstance(created, str)
                    else created
                )
                keep = h["cosine"] >= 0.85 and ms > inventory[id]["fresh_at"] - 21600000
            if keep:
                group = src["group_id"]
        row["group_id"] = str(group)
    except Exception as exc:
        row["error"] = type(exc).__name__
    return row


failed = False
with (
    outpath.open("a") as dest,
    concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool,
):
    for n, row in enumerate(pool.map(call, rows.values()), 1):
        if row:
            failed = failed or bool(row.get("error"))
            dest.write(json.dumps(row) + "\n")
            dest.flush()
        if n % 25 == 0:
            print("dedup", n, "/", len(rows), flush=True)

if failed:
    raise SystemExit(1)
