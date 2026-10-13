#!/usr/bin/env python3
"""Aggregate private replay evidence without exporting source text or queries."""

import argparse
import collections
import csv
import json
from pathlib import Path


def estimate_counts(mix, agent, history, observed, priors, strength=20):
    # Event-rate model, not LR follow-up probability. No assumed fidelity uplift.
    return sum(
        n
        * (observed.get((agent, lane), 0) + strength * priors[lane])
        / (history.get((agent, lane), 0) + strength)
        for lane, n in mix.items()
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dir", type=Path, required=True)
    parser.add_argument("--variant", choices=["summary", "combined"], default="summary")
    args = parser.parse_args()
    root = args.dir
    variant = args.variant
    manifest = json.loads(
        (root / f"counterfactual-{variant}-manifest.json").read_text()
    )
    history_data = json.loads((root / "feedback-history.json").read_text())
    observed_data = json.loads((root / "feedback-observed.json").read_text())
    history, observed = collections.Counter(), collections.Counter()
    for c in history_data["cells"]:
        history[c["agent_id"], c["lane"]] += c["deliveries"]
    for c in observed_data["cells"]:
        observed[c["agent_id"], c["lane"]] += c["positive_events"]
    priors = {
        lane: sum(v for (_, source_lane), v in observed.items() if source_lane == lane)
        / max(1, sum(v for (_, source_lane), v in history.items() if source_lane == lane))
        for lane in ("pgc", "ugc")
    }
    rows, totals = [], {arm: collections.Counter() for arm in ("before", "after")}
    for agent in manifest["agents"]:
        aid = agent["agent_id"]
        snapshot = json.loads((root / f"agent-{aid}.json").read_text())
        comparison = json.loads((root / f"comparison-{variant}-{aid}.json").read_text())
        row = {
            "agent_id": aid,
            "candidate_pool": agent["pool"],
            "captured_delivered": agent["captured_delivered"],
        }
        sets = {}
        for arm in ("before", "after"):
            candidates = comparison[arm]["candidates"] or []
            mix, basis = collections.Counter(), collections.Counter()
            sets[arm] = {c["document"]["source_ref"]["id"] for c in candidates}
            for c in candidates:
                lane = snapshot["Classes"].get(c["document"]["source_ref"]["id"])
                if lane not in ("pgc", "ugc"):
                    raise ValueError(f"Unknown source class {lane}")
                mix[lane] += 1
                origin = c["context"].get("input_origin", "unknown")
                basis[
                    {"need_input": "need", "agent_context": "profile"}.get(
                        origin, origin
                    )
                ] += 1
            row[f"{arm}_delivered"] = len(candidates)
            for lane in ("pgc", "ugc"):
                row[f"{arm}_{lane}"] = mix[lane]
            for b in ("need", "profile", "friend", "baseline", "unknown"):
                row[f"{arm}_{b}"] = basis[b]
            row[f"{arm}_expected_positive_events"] = round(
                estimate_counts(mix, aid, history, observed, priors), 6
            )
            for k, v in row.copy().items():
                if k.startswith(arm + "_"):
                    totals[arm][k[len(arm) + 1 :]] += v
        row["same_items"] = len(sets["before"] & sets["after"])
        row["historical_deliveries"] = sum(
            history[aid, lane] for lane in ("pgc", "ugc")
        )
        row["historical_positive_events"] = sum(
            observed[aid, lane] for lane in ("pgc", "ugc")
        )
        row["positive_delta"] = round(
            row["after_expected_positive_events"]
            - row["before_expected_positive_events"],
            6,
        )
        rows.append(row)
    with (root / f"per-agent-{variant}-effects.csv").open("w", newline="") as f:
        writer = csv.DictWriter(f, fieldnames=list(rows[0]))
        writer.writeheader()
        writer.writerows(rows)
    result = {
        "agents": len(rows),
        "totals": totals,
        "positive_rate_prior": priors,
        "history_window": {
            "start": history_data["delivery_start"],
            "end": history_data["delivery_end"],
        },
        "method": manifest["method"],
        "estimate": "One frozen request per Agent. Source-stratified historical positive-event/delivery rate, 20-delivery empirical-Bayes prior, no assumed benefit from improved factuality. Observed feedback includes events without a matched impression; cohort and window match, but event lag makes this an estimate, not a calibrated probability or causal guarantee.",
        "agents_losing_deliveries": sum(
            r["after_delivered"] < r["before_delivered"] for r in rows
        ),
        "agents_lower_expected_positives": sum(r["positive_delta"] < 0 for r in rows),
        "rows": rows,
    }
    (root / f"{variant}-effect-estimates.json").write_text(json.dumps(result, indent=2))
    print(json.dumps({k: v for k, v in result.items() if k != "rows"}, indent=2))


if __name__ == "__main__":
    main()
