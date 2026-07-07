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
import time
import urllib.parse
import urllib.request
from pathlib import Path


EXPECTED_SECTIONS = [
    "🎯 北极星 — 价值(信号) ▸ 护栏: 覆盖·准确·速度",
    "🔧 运维 — 系统健康",
    "🏁 首发榜单 — 对外口径 · 诊断",
]

NATURALLY_EMPTY_PANEL_IDS = {
    10,   # 异常来源榜: no rows when every failing source is already blocked or healthy.
    108,  # 即将被 block 的来源: no rows is the ideal steady state.
    407,  # SLA 破线分布: no rows is the ideal low-latency steady state.
    409,  # SLA 破线原因: no rows is ideal when there is no latency debt.
    410,  # 活跃拖慢信源: no rows is ideal when no source is currently breaching.
    412,  # 违反 source SLA 的源: no rows is the ideal source-health steady state.
    908,  # 现在先处理哪些信源: no rows is ideal when no source is active-breaching.
}

ACTIONABLE_LATENCY_PANEL_IDS = {
    33,  # 活跃高优先延迟
}

ACTIVE_SOURCE_LATENCY_PANEL_IDS = {
    62,  # 活跃延迟源明细
}

SOURCE_HEALTH_SLA_PANEL_IDS = {
    61,  # 信源 SLA
}

# 2026-07-06 语义拆分：17=火情(我方契约侧真坏的, 恒0)、61=观察清单(出版方安静/
# 加源候选/SLA, 允许有水位)。风险趋势(37)双线。63=榜单胜率(对外口径)——它已经
# 两次被改板误删, 从此由本校验器守护(见下方 OUTWARD_METRIC_MUST_EXIST)。
OWNER_COCKPIT_PANELS = {
    17: [
        "pgc_source_health_canaries_failed",
        "pgc_source_health_critical_fire",
        "pgc_signal_latency_active_source_breaches_3h",
    ],
    32: ["pgc_first_source_audit_attention"],
    33: ["pgc_signal_latency_active_source_breaches_3h"],
    34: ["pgc_source_health_canaries_failed"],
    36: ["pgc_twitterapi_credits_days_to_empty"],
    37: ["pgc_source_health_sla_attention"],
    39: ["pgc_newsapi_key_tokens_pct"],
    61: [
        "pgc_source_health_critical_watch",
        "pgc_first_source_audit_attention",
        "pgc_source_health_sla_attention",
    ],
    62: ["pgc_signal_latency_active_source_breaches_3h"],
    63: ["pgc_first_source_win_rate"],
}

# 全板必须存在的对外口径指标——不锚定到特定面板 id, 但必须有家。
# 榜单胜率 2026-07-03 和 2026-07-06 两次被改板弄丢, 每次都靠人肉体检才发现。
OUTWARD_METRIC_MUST_EXIST = [
    "pgc_first_source_win_rate",
]


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
    for panel_id, required_terms in OWNER_COCKPIT_PANELS.items():
        panel = panels_by_id.get(panel_id)
        if not panel:
            errors.append(f"missing owner cockpit panel: {panel_id}")
            continue
        exprs = [target.get("expr", "") for target in panel.get("targets", []) or []]
        joined_expr = "\n".join(exprs)
        for term in required_terms:
            if term not in joined_expr:
                errors.append(f"panel {panel_id} must use {term}")

    all_exprs = "\n".join(
        t.get("expr", "") for p in dashboard.get("panels", [])
        for t in p.get("targets", []) or [])
    for metric in OUTWARD_METRIC_MUST_EXIST:
        if metric not in all_exprs:
            errors.append(
                f"outward metric {metric} has NO panel — it went homeless twice "
                f"(2026-07-03, 2026-07-06); restore it before merging")

    for panel_id in ACTIONABLE_LATENCY_PANEL_IDS:
        panel = panels_by_id.get(panel_id)
        if not panel:
            errors.append(f"missing actionable latency panel: {panel_id}")
            continue
        exprs = [target.get("expr", "") for target in panel.get("targets", []) or []]
        if not any("pgc_signal_latency_active_source_breaches_3h" in expr for expr in exprs):
            errors.append(f"panel {panel_id} must use active source latency breaches")
        if not any("source_latency" in expr and "source_feed_lag" in expr for expr in exprs):
            errors.append(
                f"panel {panel_id} must count actionable source_latency/source_feed_lag rows"
            )

    for panel_id in ACTIVE_SOURCE_LATENCY_PANEL_IDS:
        panel = panels_by_id.get(panel_id)
        if not panel:
            errors.append(f"missing active source latency panel: {panel_id}")
            continue
        exprs = [target.get("expr", "") for target in panel.get("targets", []) or []]
        if not any("pgc_signal_latency_active_source_breaches_3h" in expr for expr in exprs):
            errors.append(f"panel {panel_id} must use active source latency metric")
        if not any("source_latency" in expr and "source_feed_lag" in expr for expr in exprs):
            errors.append(
                f"panel {panel_id} must focus on actionable source_latency/source_feed_lag rows"
            )

    for panel_id in SOURCE_HEALTH_SLA_PANEL_IDS:
        panel = panels_by_id.get(panel_id)
        if not panel:
            errors.append(f"missing source health SLA panel: {panel_id}")
            continue
        exprs = [target.get("expr", "") for target in panel.get("targets", []) or []]
        if not any("pgc_source_health_sla_attention" in expr for expr in exprs):
            errors.append(f"panel {panel_id} must use source health SLA attention metric")

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
    empty_retries: int,
    empty_retry_delay: float,
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
        payload = None
        result = []
        query_error = False
        for attempt in range(empty_retries + 1):
            try:
                payload = query_prometheus(prom_expr, prometheus_url, ssh_host)
            except Exception as exc:
                errors.append(f"{panel.get('id')} {panel.get('title')}: query failed: {exc}")
                query_error = True
                break
            if payload.get("status") != "success":
                errors.append(f"{panel.get('id')} {panel.get('title')}: {payload}")
                query_error = True
                break
            result = payload.get("data", {}).get("result", [])
            if result or allow_empty or panel.get("id") in NATURALLY_EMPTY_PANEL_IDS:
                break
            if attempt < empty_retries:
                time.sleep(empty_retry_delay)
        if query_error:
            continue
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
    parser.add_argument("--empty-retries", default=2, type=int)
    parser.add_argument("--empty-retry-delay", default=2.0, type=float)
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
                args.empty_retries,
                args.empty_retry_delay,
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
