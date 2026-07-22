#!/usr/bin/env python3
"""Evaluate every thresholded PGC dashboard panel against live Prometheus and
print the color each one is showing RIGHT NOW.

The alert plane (alertmanager + rules) and the panel-color plane are two
independent judgments: zero firing alerts does not mean zero red panels
(2026-07-22 patrol gap — the owner saw red/yellow on a board that had just
been reported "all green"). Ops patrols must sweep BOTH; this script is the
panel-color half. The structural gate stays in
validate_pgc_grafana_dashboard.py — this tool only reads live values.

Usage (from repo root):
    python3 scripts/local/eval_pgc_dashboard_colors.py \
        --ssh-host aliapmo --prometheus-url http://localhost:9091
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from validate_pgc_grafana_dashboard import query_prometheus  # noqa: E402

DASHBOARD = Path(__file__).resolve().parents[2] / (
    "configs/grafana/dashboards/pgc-pipeline.json"
)

_ICONS = {"red": "🔴", "orange": "🟠", "yellow": "🟡", "green": "🟢", "blue": "🔵"}


def color_for(value: float, steps: list[dict]) -> str:
    current = steps[0].get("color", "?")
    for step in steps[1:]:
        threshold = step.get("value")
        if threshold is not None and value >= threshold:
            current = step.get("color", current)
    return current


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--prometheus-url", required=True)
    parser.add_argument("--ssh-host")
    parser.add_argument("--rate-window", default="5m")
    args = parser.parse_args()

    dashboard = json.loads(DASHBOARD.read_text())
    attention = 0
    for panel in dashboard.get("panels", []):
        if panel.get("type") not in ("stat", "bargauge", "gauge"):
            continue
        steps = (
            panel.get("fieldConfig", {})
            .get("defaults", {})
            .get("thresholds", {})
            .get("steps", [])
        )
        exprs = [t["expr"] for t in panel.get("targets", []) if t.get("expr")]
        if not steps or not exprs:
            continue
        print(f"[{panel.get('id')}] {panel.get('title')}")
        for expr in exprs:
            prom_expr = expr.replace("$__rate_interval", args.rate_window)
            try:
                result = query_prometheus(
                    prom_expr, args.prometheus_url, args.ssh_host
                )["data"]["result"]
            except Exception as exc:  # noqa: BLE001 — report and keep sweeping
                print(f"  ⚠️ query failed: {exc}")
                attention += 1
                continue
            if not result:
                print(f"  · no data  <- {expr[:90]}")
                continue
            for series in result:
                value = float(series["value"][1])
                labels = ",".join(
                    f"{k}={v}"
                    for k, v in series.get("metric", {}).items()
                    if k not in ("__name__", "instance", "job")
                )
                color = color_for(value, steps)
                if color in ("red", "orange", "yellow"):
                    attention += 1
                icon = _ICONS.get(color, f"[{color}]")
                print(f"  {icon} {value:g}  {labels}")
        print()
    print(f"{attention} value(s) off green — investigate each before calling "
          "the board healthy; 0 alerts firing is NOT the same claim.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
