#!/usr/bin/env python3
"""Publish anonymous fixed-cohort effects; keep IDs, contexts and items private."""
import argparse
import collections
import csv
import hashlib
import json
from pathlib import Path


def age_bucket(days):
    return "over30" if days > 30 else "7to30" if days > 7 else "under7"


def delivered_identities(arm):
    items, friends = set(), set()
    for step in arm["Steps"]:
        for candidate in step["Delivered"] or []:
            doc = candidate["document"]
            identity = (doc["source_ref"]["type"], str(doc["source_ref"]["id"]))
            items.add(identity)
            if "friend" in (doc.get("channels") or []):
                friends.add(identity)
    return items, friends


def rates(history):
    by_agent, by_source, totals = {}, collections.Counter(), collections.Counter()
    for c in history:
        aid, lane, age = str(c["agent_id"]), c["lane"], c["age"]
        d, p = c["deliveries"], c["positives"]
        by_agent[aid, lane, age] = (d, p)
        by_source[aid, lane, "d"] += d
        by_source[aid, lane, "p"] += p
        totals[lane, "d"] += d
        totals[lane, "p"] += p
        totals[lane, age, "d"] += d
        totals[lane, age, "p"] += p
    return by_agent, by_source, totals


def expected(aid, mix, history):
    by_agent, by_source, totals = history
    source_estimate = age_estimate = 0.0
    for (lane, age), n in mix.items():
        if not totals[lane, "d"] or not totals[lane, age, "d"]:
            raise ValueError("missing historical support; no invented positive rate")
        source_rate = (by_source[aid, lane, "p"] + 20 * totals[lane, "p"] / totals[lane, "d"]) / (by_source[aid, lane, "d"] + 20)
        d, p = by_agent.get((aid, lane, age), (0, 0))
        age_rate = (p + 20 * totals[lane, age, "p"] / totals[lane, age, "d"]) / (d + 20)
        source_estimate += n * source_rate
        age_estimate += n * age_rate
    return source_estimate, age_estimate


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dir", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    root = args.dir
    manifest = json.loads((root / "refill-manifest.json").read_text())
    cohort = json.loads((root / "cohort.json").read_text())
    for name, digest in manifest["input_sha256"].items():
        if hashlib.sha256((root / name).read_bytes()).hexdigest() != digest:
            raise ValueError("replay inputs changed before report")
    if len(cohort) != 225 or len({c["AgentID"] for c in cohort}) != 225 or manifest["aa_equal"] != 225:
        raise ValueError("fixed 225-Agent replay incomplete")
    history = rates([json.loads(line) for line in (root / "rate-history.jsonl").read_text().splitlines()])
    rows, totals = [], {a: collections.Counter() for a in ("before", "after")}
    uncensored_totals = {a: collections.Counter() for a in totals}
    uncensored_agents = 0
    by_step = {a: [collections.Counter() for _ in range(24)] for a in totals}
    rejections = {a: collections.Counter() for a in totals}
    first_rejections = {a: collections.Counter() for a in totals}
    lexical_head = collections.Counter()
    censor_reasons = {a: collections.Counter() for a in totals}
    captures, capture_ends, hashes = [], [], []
    for c in cohort:
        aid = str(c["AgentID"])
        raw = (root / f"refill-agent-{aid}.json").read_bytes()
        s = json.loads(raw)
        if s["CaptureError"]:
            raise ValueError("failed capture in fixed cohort")
        hashes.append(hashlib.sha256(raw).hexdigest())
        for channel in s["Channels"].values():
            if channel["Name"] != "lexical":
                continue
            for d in channel["Documents"] or []:
                lexical_head["rows"] += 1
                key = d["source_ref"]["type"] + ":" + d["source_ref"]["id"]
                hydrated = s["Hydrated"].get(key)
                if hydrated is None:
                    lexical_head["missing_source"] += 1
                elif d["version"] != hydrated["version"]:
                    lexical_head["source_version"] += 1
                elif s["Seen"][key]:
                    lexical_head["seen"] += 1
                else:
                    lexical_head["remaining_before_other_gates"] += 1
                if hydrated is not None and d.get("group_id", 0) != hydrated.get("group_id", 0):
                    lexical_head["es_forward_group_disagreements"] += 1
        captures.append(s["At"])
        capture_ends.append(s["At"] + s["CaptureMS"])
        result = json.loads((root / f"refill-comparison-{aid}.json").read_text())
        if result["Owner"] != c["AgentID"]:
            raise ValueError("replay owner mismatch")
        row = {"agent": hashlib.sha256(("consumed-refill-20261014:" + aid).encode()).hexdigest()[:12]}
        paired_complete = not (result["Before"]["PoolIncomplete"] or result["After"]["PoolIncomplete"])
        uncensored_agents += int(paired_complete)
        baseline_items, baseline_friends = delivered_identities(result["Before"])
        for arm in totals:
            data = result[arm.capitalize()]
            if [step["Step"] for step in data["Steps"]] != list(range(1, 25)):
                raise ValueError("incomplete sequence")
            mix, counts = collections.Counter(), collections.Counter()
            delivered_items, delivered_friends = delivered_identities(data)
            counts["friend_source"] = len(delivered_friends)
            counts["retained_baseline_items"] = len(baseline_items & delivered_items)
            counts["retained_baseline_friends"] = len(baseline_friends & delivered_items)
            delivered_groups = set()
            for step in data["Steps"]:
                candidates = step["Delivered"] or []
                by_step[arm][step["Step"] - 1]["delivered"] += len(candidates)
                if not candidates:
                    counts["empty_requests"] += 1
                    by_step[arm][step["Step"] - 1]["empty_agents"] += 1
                for candidate in candidates:
                    d = candidate["document"]
                    item_id = d["source_ref"]["id"]
                    lane = s["Classes"][item_id]
                    if lane not in ("pgc", "ugc"):
                        raise ValueError("unknown content class")
                    group = d.get("group_id")
                    if group and group in delivered_groups:
                        raise ValueError("duplicate delivered group")
                    if group:
                        delivered_groups.add(group)
                    if d.get("fresh_at", 0) <= 0:
                        raise ValueError("missing content age")
                    days = max(0, (s["At"] - d["fresh_at"]) / 86400000)
                    age = age_bucket(days)
                    mix[lane, age] += 1
                    basis = {"agent_context": "profile", "need_input": "need"}.get(candidate["context"]["input_origin"], candidate["context"]["input_origin"])
                    counts["delivered"] += 1
                    counts[lane] += 1
                    counts[f"{lane}_{age}"] += 1
                    counts[basis] += 1
                    counts[age] += 1
                    counts["age_sum_days"] += days
                    by_step[arm][step["Step"] - 1][lane] += 1
                    by_step[arm][step["Step"] - 1][basis] += 1
                if step["Refill"]:
                    counts["refill_requests"] += 1
                    counts["refill_recalled"] += step["Refill"]["recalled"]
                    counts["refill_eligible"] += step["Refill"]["eligible"]
                if step["PartialReasons"]:
                    counts["partial_requests"] += 1
            source, age = expected(aid, mix, history)
            counts["expected_positive_events_source"] = source
            counts["expected_positive_events_source_age"] = age
            counts["censored_agents"] = int(data["PoolIncomplete"])
            reasons = data.get("PoolIncompleteReasons") or []
            row[f"{arm}_censor_reasons"] = ";".join(reasons)
            censor_reasons[arm].update(reasons)
            counts["zero_delivery_agents"] = int(counts["delivered"] == 0)
            rejections[arm].update(data["Rejections"])
            first_rejections[arm].update(data["Steps"][0]["Rejections"])
            for field in ("delivered", "pgc", "ugc", "need", "profile", "friend", "friend_source", "retained_baseline_items", "retained_baseline_friends", "baseline", "under7", "7to30", "over30", "empty_requests", "partial_requests", "censored_agents", "expected_positive_events_source", "expected_positive_events_source_age"):
                row[f"{arm}_{field}"] = round(counts[field], 5)
            totals[arm].update(counts)
            if paired_complete:
                uncensored_totals[arm].update(counts)
        row["delivery_delta"] = row["after_delivered"] - row["before_delivered"]
        row["positive_estimate_delta"] = round(row["after_expected_positive_events_source"] - row["before_expected_positive_events_source"], 5)
        rows.append(row)
    snapshot_digest = hashlib.sha256("\n".join(hashes).encode()).hexdigest()
    if snapshot_digest != manifest["snapshots_sha256"]:
        raise ValueError("captured snapshots changed after replay")
    args.out.mkdir(parents=True, exist_ok=True)
    with (args.out / "fixed-225-effects.csv").open("w") as f:
        w = csv.DictWriter(f, fieldnames=list(rows[0]), lineterminator="\n")
        w.writeheader()
        w.writerows(rows)
    summary = {
        "manifest": manifest,
        "capture_start_ms": min(captures), "capture_end_ms": max(capture_ends),
        "snapshots_sha256": snapshot_digest,
        "totals": totals, "by_step": by_step, "rejections": rejections,
        "uncensored_agents": uncensored_agents, "uncensored_totals": uncensored_totals,
        "censored_agent_reasons": censor_reasons,
        "coverage_limit": "All 225 Agents remain in the primary report. Any censored arm is an unvalidated scenario estimate; inspect the separately reported complete pairs and do not use aggregate fill gains to waive the missing coverage.",
        "first_request_rejections": first_rejections, "initial_lexical_head": lexical_head,
        "stage_count_semantics": "Initial lexical counts are candidate/context rows before other-channel merge; first-request Engine rejection events include all channels and context duplication. They are diagnostic counts, not one additive unique-item funnel.",
        "agents_more": sum(r["delivery_delta"] > 0 for r in rows),
        "agents_less": sum(r["delivery_delta"] < 0 for r in rows),
        "agents_equal": sum(r["delivery_delta"] == 0 for r in rows),
        "agents_missing_baseline_items": sum(r["after_retained_baseline_items"] < r["before_delivered"] for r in rows),
        "agents_missing_baseline_friends": sum(r["after_retained_baseline_friends"] < r["before_friend_source"] for r in rows),
        "friend_count_semantics": "friend counts winning Friend contexts; friend_source counts the unioned friend provenance independent of winning Need/Profile context. Retention compares item identities, not attribution labels, within each Agent's 24-request scenario.",
        "positive_estimate_limit": "Historical event-rate sensitivity, not calibrated probability or causal daily uplift; unmatched/delayed feedback, exposure selection and frozen supply limit validity.",
        "historical_rates": {":".join(k): v for k, v in history[2].items()},
    }
    (args.out / "fixed-225-summary.json").write_text(json.dumps(summary, indent=2))
    print(json.dumps({"totals": totals, "agents_more": summary["agents_more"], "agents_less": summary["agents_less"]}, indent=2))

if __name__ == "__main__":
    main()
