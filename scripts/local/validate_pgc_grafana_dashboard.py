#!/usr/bin/env python3
"""Validate the provisioned PGC Grafana dashboard.

By default this performs static checks on the dashboard JSON. With --ssh-host it
also evaluates every Prometheus panel expression against that host's local
Prometheus API, replacing Grafana's $__rate_interval macro with --rate-window.
"""

from __future__ import annotations

import argparse
import json
import shlex
import subprocess
import sys
import urllib.parse
import urllib.request
from pathlib import Path


EXPECTED_SECTIONS = [
    "一手有没有漏 / First-Source Coverage",
    "信号够不够快 / Signal Latency",
    "每条信号卡在哪一跳 / Event Timeline",
    "信源是否可靠 / Source Reliability",
    "内容有没有送达 / Delivery",
    "生产链路是否健康 / Pipeline Health",
    "质量和成本是否失控 / Quality & Cost",
    "工程诊断 / Deep Dive",
]

NATURALLY_EMPTY_PANEL_IDS = {
    10,   # 异常来源榜: no rows when every failing source is already blocked or healthy.
    108,  # 即将被 block 的来源: no rows is the ideal steady state.
    407,  # SLA 破线分布: no rows is the ideal low-latency steady state.
    409,  # SLA 破线原因: no rows is ideal when there is no latency debt.
    410,  # 活跃拖慢信源: no rows is ideal when no source is currently breaching.
}

ACTIONABLE_LATENCY_PANEL_IDS = {
    401,  # 现在高优先级信号还在超时吗
    407,  # 哪些类别需要马上处理
}

BREACH_KIND_PANEL_IDS = {
    409,  # 这些超时是事故还是回补噪音
}

ACTIVE_SOURCE_LATENCY_PANEL_IDS = {
    410,  # 当前哪些信源正在拖慢
}


def load_dashboard(path: Path) -> dict:
    try:
        return json.loads(path.read_text())
    except Exception as exc:  # pragma: no cover - kept user-facing.
        raise SystemExit(f"failed to load dashboard JSON: {exc}") from exc


def iter_targets(dashboard: dict):
    for panel in dashboard.get("panels", []):
        for target in panel.get("targets", []) or []:
            expr = target.get("expr")
            if expr:
                yield panel, target, expr


def static_validate(dashboard: dict) -> list[str]:
    errors: list[str] = []
    if dashboard.get("uid") != "pgc-pipeline":
        errors.append("dashboard uid must be pgc-pipeline")
    if dashboard.get("title") != "EigenFlux - PGC Pipeline":
        errors.append("dashboard title must be EigenFlux - PGC Pipeline")

    row_titles = [p.get("title") for p in dashboard.get("panels", []) if p.get("type") == "row"]
    for section in EXPECTED_SECTIONS:
        if section not in row_titles:
            errors.append(f"missing section row: {section}")

    panels_by_id = {panel.get("id"): panel for panel in dashboard.get("panels", [])}
    for panel_id in ACTIONABLE_LATENCY_PANEL_IDS:
        panel = panels_by_id.get(panel_id)
        if not panel:
            errors.append(f"missing actionable latency panel: {panel_id}")
            continue
        exprs = [target.get("expr", "") for target in panel.get("targets", []) or []]
        if not any("pgc_signal_latency_actionable_breaches_3h" in expr for expr in exprs):
            errors.append(f"panel {panel_id} must use active actionable latency breaches")

    for panel_id in BREACH_KIND_PANEL_IDS:
        panel = panels_by_id.get(panel_id)
        if not panel:
            errors.append(f"missing breach-kind latency panel: {panel_id}")
            continue
        exprs = [target.get("expr", "") for target in panel.get("targets", []) or []]
        if not any("pgc_signal_latency_breach_kind_24h" in expr for expr in exprs):
            errors.append(f"panel {panel_id} must use breach-kind latency metric")

    for panel_id in ACTIVE_SOURCE_LATENCY_PANEL_IDS:
        panel = panels_by_id.get(panel_id)
        if not panel:
            errors.append(f"missing active source latency panel: {panel_id}")
            continue
        exprs = [target.get("expr", "") for target in panel.get("targets", []) or []]
        if not any("pgc_signal_latency_active_source_breaches_3h" in expr for expr in exprs):
            errors.append(f"panel {panel_id} must use active source latency metric")
        if not any('kind="source_latency"' in expr for expr in exprs):
            errors.append(f"panel {panel_id} must focus on actionable source_latency rows")

    prometheus_targets = 0
    loki_targets = 0
    for panel in dashboard.get("panels", []):
        datasource = panel.get("datasource") or {}
        targets = panel.get("targets") or []
        if not targets:
            continue
        uid = datasource.get("uid")
        ptype = panel.get("type")
        if ptype == "logs":
            loki_targets += len(targets)
            if uid != "loki":
                errors.append(f"panel {panel.get('id')} {panel.get('title')} must use loki")
        else:
            prometheus_targets += len(targets)
            if uid != "pgc-prometheus":
                errors.append(
                    f"panel {panel.get('id')} {panel.get('title')} must use pgc-prometheus"
                )

    if prometheus_targets < 20:
        errors.append(f"expected at least 20 Prometheus targets, found {prometheus_targets}")
    if loki_targets < 1:
        errors.append("expected at least one Loki target")
    return errors


def query_prometheus(expr: str, prometheus_url: str, ssh_host: str | None) -> dict:
    query_url = f"{prometheus_url.rstrip('/')}/api/v1/query"
    if ssh_host:
        remote = (
            f"curl -fsS -G {shlex.quote(query_url)} --data-urlencode "
            f"{shlex.quote('query=' + expr)}"
        )
        proc = subprocess.run(
            ["ssh", ssh_host, remote],
            check=False,
            capture_output=True,
            text=True,
            timeout=30,
        )
        if proc.returncode != 0:
            raise RuntimeError(proc.stderr.strip() or proc.stdout.strip())
        return json.loads(proc.stdout)

    data = urllib.parse.urlencode({"query": expr}).encode()
    req = urllib.request.Request(query_url, data=data, method="POST")
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read().decode())


def prometheus_validate(
    dashboard: dict,
    prometheus_url: str,
    ssh_host: str | None,
    rate_window: str,
    allow_empty: bool,
    panel_ids: set[int] | None,
) -> list[str]:
    errors: list[str] = []
    checked = 0
    for panel, _target, expr in iter_targets(dashboard):
        if panel.get("type") == "logs":
            continue
        if panel_ids is not None and panel.get("id") not in panel_ids:
            continue
        checked += 1
        prom_expr = expr.replace("$__rate_interval", rate_window)
        try:
            payload = query_prometheus(prom_expr, prometheus_url, ssh_host)
        except Exception as exc:
            errors.append(f"{panel.get('id')} {panel.get('title')}: query failed: {exc}")
            continue
        if payload.get("status") != "success":
            errors.append(f"{panel.get('id')} {panel.get('title')}: {payload}")
            continue
        result = payload.get("data", {}).get("result", [])
        if not result and not allow_empty and panel.get("id") not in NATURALLY_EMPTY_PANEL_IDS:
            errors.append(f"{panel.get('id')} {panel.get('title')}: empty result for {prom_expr}")
    if checked == 0:
        errors.append("no Prometheus expressions found")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--dashboard",
        default="configs/grafana/dashboards/pgc-pipeline.json",
        type=Path,
    )
    parser.add_argument("--prometheus-url")
    parser.add_argument("--ssh-host")
    parser.add_argument("--rate-window", default="5m")
    parser.add_argument("--allow-empty", action="store_true")
    parser.add_argument(
        "--panel-id",
        action="append",
        default=[],
        type=int,
        help="Only run production Prometheus validation for this panel id. Repeatable.",
    )
    args = parser.parse_args()

    dashboard = load_dashboard(args.dashboard)
    errors = static_validate(dashboard)
    if args.prometheus_url:
        panel_ids = set(args.panel_id) if args.panel_id else None
        errors.extend(
            prometheus_validate(
                dashboard,
                args.prometheus_url,
                args.ssh_host,
                args.rate_window,
                args.allow_empty,
                panel_ids,
            )
        )

    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1

    prom_count = sum(1 for p, _t, _e in iter_targets(dashboard) if p.get("type") != "logs")
    print(
        f"OK: {dashboard.get('title')} panels={len(dashboard.get('panels', []))} "
        f"prometheus_targets={prom_count}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
