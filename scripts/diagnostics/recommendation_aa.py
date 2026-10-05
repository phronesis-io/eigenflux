#!/usr/bin/env python3
"""Offline, read-only summary of bounded recommendation A/A log exports."""
import argparse
import collections
import hashlib
import json
import math
from pathlib import Path

EXPERIMENT = "recommendation_aa_v1"
OUTCOMES = {"empty", "nonempty", "http_error", "business_error", "invalid_response", "in_progress"}


def arm(agent_id):
    return "a1" if hashlib.sha256(f"{EXPERIMENT}:{agent_id}".encode()).digest()[0] % 2 == 0 else "a2"


def srm_p_value(a1, a2):
    """Two-sided exact binomial test on enrolled unique Agents, not requests."""
    n = a1 + a2
    if not n:
        return None
    k = min(a1, a2)
    if k * 2 == n:
        return 1.0
    log_prob = math.lgamma(n + 1) - math.lgamma(k + 1) - math.lgamma(n - k + 1) - n * math.log(2)
    relative_sum, term = 1.0, 1.0
    for j in range(k, 0, -1):
        term *= j / (n - j + 1)
        relative_sum += term
    return min(1.0, 2 * math.exp(log_prob) * relative_sum)


def summarize(rows, expected_requests=None):
    unique = {}
    duplicates = 0
    for entry in rows:
        o = entry.get("observation", entry)
        if o.get("experiment_id") != EXPERIMENT:
            raise ValueError("export contains a different experiment")
        agent_id = o.get("agent_id")
        if not isinstance(agent_id, str) or not agent_id.isascii() or not agent_id.isdecimal() or int(agent_id) <= 0 or str(int(agent_id)) != agent_id:
            raise ValueError("Agent IDs must be positive decimal strings")
        if o.get("arm") != arm(agent_id):
            raise ValueError("unstable or incorrect Agent allocation")
        if o.get("outcome") not in OUTCOMES or not o.get("observation_id"):
            raise ValueError("missing observation identity or invalid outcome")
        count = o.get("item_count")
        successful = o["outcome"] in {"empty", "nonempty"}
        if successful and (type(count) is not int or count < 0 or (count == 0) != (o["outcome"] == "empty")):
            raise ValueError("successful completion has an inconsistent item count")
        if not successful and count is not None:
            raise ValueError("failed completion must retain unknown item count")
        observation_id = o["observation_id"]
        if observation_id in unique:
            if unique[observation_id] != o:
                raise ValueError("conflicting observations share an identity")
            duplicates += 1
        unique[observation_id] = o
    by_agent = collections.defaultdict(list)
    strata = collections.Counter()
    for o in unique.values():
        by_agent[o["agent_id"]].append(o)
        strata[(o["endpoint"], o["caller"], o["pipeline_version"], o["outcome"])] += 1
    buckets = {a: [events for agent_id, events in by_agent.items() if arm(agent_id) == a] for a in ("a1", "a2")}
    arms = {}
    for a, agents in buckets.items():
        events = [o for group in agents for o in group]
        arms[a] = {
            "unique_agents": len(agents), "requests": len(events),
            "outcomes": dict(collections.Counter(o["outcome"] for o in events)),
            "agent_weighted_empty_rate": sum(sum(o["outcome"] == "empty" for o in group) / len(group) for group in agents) / len(agents) if agents else None,
            "agent_weighted_in_progress_rate": sum(sum(o["outcome"] == "in_progress" for o in group) / len(group) for group in agents) / len(agents) if agents else None,
            "agent_weighted_error_rate": sum(sum(o["outcome"] in {"http_error", "business_error"} for o in group) / len(group) for group in agents) / len(agents) if agents else None,
        }
    p = srm_p_value(arms["a1"]["unique_agents"], arms["a2"]["unique_agents"])
    issues = []
    if expected_requests is None:
        issues.append("coverage_unverified")
    elif expected_requests != len(unique):
        issues.append("completion_counter_export_mismatch")
    if not by_agent:
        issues.append("no_enrolled_agents")
    if p is not None and p < 0.001:
        issues.append("sample_ratio_mismatch")
    missing = sum(o["outcome"] == "nonempty" and not o.get("impression_id") for o in unique.values())
    if missing:
        issues.append("nonempty_without_impression_id")
    if any(o["outcome"] == "invalid_response" for o in unique.values()):
        issues.append("unclassified_response")
    return {
        "experiment_id": EXPERIMENT, "status": "observation_only", "requests": len(unique),
        "duplicate_export_rows": duplicates, "measurement_issues": issues, "arms": arms,
        "unique_agent_srm_p": p, "nonempty_without_impression_id": missing,
        "strata": [dict(zip(("endpoint", "caller", "pipeline_version", "outcome"), key), requests=count) for key, count in sorted(strata.items())],
        "interpretation": "A/A measurements do not establish recommendation lift. No passing decision is inferred from a nonsignificant result.",
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("export", type=Path, help="JSONL: one structured log or observation per line")
    parser.add_argument("--expected-requests", type=int, help="independently verified completion count for the exact same scope/window")
    args = parser.parse_args()
    if args.expected_requests is not None and args.expected_requests < 0:
        parser.error("expected requests must be nonnegative")
    try:
        with args.export.open() as source:
            rows = [json.loads(line) for line in source if line.strip()]
        print(json.dumps(summarize(rows, args.expected_requests), indent=2))
    except (ValueError, KeyError, TypeError) as exc:
        parser.error(str(exc))


if __name__ == "__main__":
    main()
