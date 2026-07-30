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


def series_name(target: dict, series: dict) -> str:
    """The name Grafana matches byName overrides against.

    legendFormat wins when it has no {{label}} template in it, which is the
    only form our panels use for a fixed second series (e.g. "状态").
    """
    legend = (target.get("legendFormat") or "").strip()
    if legend and "{{" not in legend:
        return legend
    return series.get("metric", {}).get("__name__", "")


def apply_overrides(
    panel: dict,
    name: str,
    steps: list[dict],
    mappings: list,
    color: dict,
):
    """Resolve per-series overrides the way Grafana does.

    Added 2026-07-28: without this the tool judged EVERY series by the panel's
    default thresholds, so a mapped status series ("0 = normal") was reported
    as a red 0 next to a perfectly healthy star. A colour checker that cannot
    see overrides reports colours the board does not have — which is the same
    class of blind check this whole sweep was about.
    """
    for override in panel.get("fieldConfig", {}).get("overrides", []) or []:
        matcher = override.get("matcher") or {}
        if matcher.get("id") != "byName" or matcher.get("options") != name:
            continue
        for prop in override.get("properties", []) or []:
            if prop.get("id") == "thresholds":
                steps = (prop.get("value") or {}).get("steps", steps)
            elif prop.get("id") == "mappings":
                mappings = prop.get("value") or mappings
            elif prop.get("id") == "color":
                color = prop.get("value") or color
    return steps, mappings, color


def mapped(value: float, mappings: list):
    """Value mappings win over thresholds for both text and colour."""
    for mapping in mappings or []:
        opts = mapping.get("options") or {}
        if mapping.get("type") == "value":
            for raw, spec in opts.items():
                try:
                    if float(raw) == value:
                        return spec.get("text"), spec.get("color")
                except (TypeError, ValueError):
                    continue
        elif mapping.get("type") == "range":
            lo, hi = opts.get("from"), opts.get("to")
            if (lo is None or value >= lo) and (hi is None or value <= hi):
                spec = opts.get("result") or {}
                return spec.get("text"), spec.get("color")
    return None, None


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
        default_mappings = (
            panel.get("fieldConfig", {}).get("defaults", {}).get("mappings", []) or []
        )
        default_color = (
            panel.get("fieldConfig", {}).get("defaults", {}).get("color", {}) or {}
        )
        targets = [t for t in panel.get("targets", []) if t.get("expr")]
        if not targets:
            continue
        print(f"[{panel.get('id')}] {panel.get('title')}")
        for target in targets:
            expr = target["expr"]
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
                name = series_name(target, series)
                s_steps, s_maps, s_color = apply_overrides(
                    panel, name, steps, default_mappings, default_color
                )
                text, mapped_color = mapped(value, s_maps)
                if mapped_color:
                    color = mapped_color
                elif s_color.get("mode") == "fixedColor":
                    color = s_color.get("fixedColor", "text")
                elif s_steps:
                    color = color_for(value, s_steps)
                else:
                    continue
                if color in ("red", "orange", "yellow"):
                    attention += 1
                icon = _ICONS.get(color, f"[{color}]")
                shown = f"{value:g}" if text is None else f"{text} ({value:g})"
                print(f"  {icon} {shown}  {labels}")
        print()
    print(f"{attention} value(s) off green — investigate each before calling "
          "the board healthy; 0 alerts firing is NOT the same claim.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
