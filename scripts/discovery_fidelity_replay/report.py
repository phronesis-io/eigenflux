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
    parser.add_argument("--variant", choices=["summary", "combined", "source-context", "slop", "new-supply", "new-supply-refreshed"], default="summary")
    args = parser.parse_args()
    root = args.dir
    variant = args.variant
    manifest = json.loads(
        (root / f"counterfactual-{variant}-manifest.json").read_text()
    )
    history_data = json.loads((root / "feedback-history.json").read_text())
    observed_data = (history_data if variant.startswith("new-supply") else json.loads((root / "feedback-observed.json").read_text()))
    history, observed = collections.Counter(), collections.Counter()
    for c in history_data["cells"]:
        history[c["agent_id"], c["lane"]] += c["deliveries"]
    for c in observed_data["cells"]:
        observed[c["agent_id"], c["lane"]] += c["positive_events"]
    if variant.startswith("new-supply"):
        for lane in ("pgc", "ugc"):
            if sum(v for (_, source_lane), v in history.items() if source_lane == lane) <= 0:
                raise ValueError(f"Missing historical exposure support for {lane}")
    priors = {
        lane: sum(v for (_, source_lane), v in observed.items() if source_lane == lane)
        / max(1, sum(v for (_, source_lane), v in history.items() if source_lane == lane))
        for lane in ("pgc", "ugc")
    }
    rows, totals = [], {arm: collections.Counter() for arm in ("before", "after")}
    for agent in manifest["agents"]:
        aid = agent["agent_id"]
        snapshot = json.loads((root / f"agent-{aid}.json").read_text())
        snapshot["Classes"].update(manifest.get("new_supply_classes", {}))
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
        new_ids = set(manifest.get("new_supply_classes", {}))
        row["new_supply_delivered"] = len(sets["after"] & new_ids)
        for lane in ("pgc", "ugc"):
            row[f"displaced_{lane}"] = sum(snapshot["Classes"].get(i) == lane for i in sets["before"] - sets["after"])
        row["new_supply_ids"] = ";".join(sorted(sets["after"] & new_ids))
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
        "estimate": ("One frozen Feed refresh per Agent. Matched positive-event/delivery rates from the same historical impression window, 20-delivery cohort prior; no factuality uplift assumed. Feedback lag and exposure selection prevent interpreting this as calibrated probability or causal daily uplift." if variant.startswith("new-supply") else "One frozen request per Agent. Source-stratified historical positive-event/delivery rate, 20-delivery empirical-Bayes prior, no assumed benefit from improved factuality. Observed feedback includes events without a matched impression; cohort and window match, but event lag makes this an estimate, not a calibrated probability or causal guarantee."),
        "new_supply_delivered": sum(r["new_supply_delivered"] for r in rows),
        "displaced": {lane: sum(r[f"displaced_{lane}"] for r in rows) for lane in ("pgc", "ugc")},
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
