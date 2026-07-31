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


# 2026-07-31 重排：每一层都用老板能直接回答的问题命名，并把模糊总数
# 下钻到具体来源和具体报道。
EXPECTED_SECTIONS = [
    "结果 — 今天做成了什么",
    "质量 — 今天有没有伤害用户",
    "行动 — 现在具体要处理什么",
    "漏文 — 到底漏了什么，为什么",
    "可信度 — 这些数字能不能信",
    "趋势 — 最近有没有变好",
    "运行 — 信源、积压与额度是否健康",
]

# Panels whose empty prod result is the ideal steady state (e.g. a failure list).
# 2026-07-08: emptied — every previous entry was a 76-panel-era id that no longer
# exists; a reused id would silently inherit the exemption. Add ids back only for
# panels that exist AND are legitimately empty when healthy.
NATURALLY_EMPTY_PANEL_IDS = {
    80,  # 没有任何来源延迟时，明细表为空就是健康
    81,  # 没有一手来源空缺线索时，明细表为空就是健康
    82,  # 没有当前信源问题时，明细表为空就是健康
}

ACTIONABLE_LATENCY_PANEL_IDS = {
    33,  # 活跃高优先延迟
}

# 2026-07-31: 80 恢复来源级明细。用户不应为了理解一个总数去翻日志。
ACTIVE_SOURCE_LATENCY_PANEL_IDS = {
    80,
}

SOURCE_HEALTH_SLA_PANEL_IDS = {
    23,
}

# 2026-07-06 语义拆分：17=待处理故障（我方契约侧真坏的，恒 0）。
# 2026-07-31: 删除把多种原因揉成一个数的 61，来源现状集中到 23，
# 具体迟到与一手源空缺分别由 80/81 展开。
# 63=抢先率（对外口径）(旧名 榜单胜率)——它已经两次被改板误删, 从此由本校验器守护
# (见下方 OUTWARD_METRIC_MUST_EXIST)。
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
    78: ["pgc_newsapi_key_tokens_pct", "pgc_scraperapi_credits_pct"],
    23: [
        "pgc_source_health_active_sources",
        "pgc_source_health_configured_sources",
        "pgc_source_health_blocked_sources",
        "pgc_source_health_failing_high_sources",
        "pgc_source_health_critical_watch",
        "pgc_source_health_sla_attention",
    ],
    64: ["pgc_newsapi_cursor_lag_hours"],
    63: ["pgc_first_source_win_rate"],
    # 2026-07-28 新锚：北极星与「测量本身还在跑吗」。前者删掉=看板失去主指标，
    # 后者删掉=又回到审计静默停摆两天没人知道的状态。
    19: ["pgc_event_real_wins", "pgc_north_star_state"],
    74: ["pgc_audit_last_success_timestamp"],
    73: ["pgc_broadcast_unfaithful_24h"],
    79: ["pgc_broadcast_faithfulness_weighted_pct"],
    80: ["pgc_signal_latency_active_source_breaches_3h"],
    81: ["pgc_first_source_audit_attention_item_info"],
    82: ["pgc_source_health_problem_source_info"],
    76: ["pgc_loss_ledger_events"],
    77: ["pgc_metric_value", "pgc_metric_sample_size", "pgc_metric_ci_halfwidth"],
    53: ["pgc_discard_audit_dual_review"],
}

# 诚实契约闸(2026-07-28): 抽样得出的比率，必须能在板上看到它的样本量，否则
# 读者无法判断一次波动是真的变化还是噪声。实测教训：忠实率 87% 对目标线 90%、
# n=300 时误差 ±3.8 点——两者统计上分不开，而面板上完全看不出来。
# 值 -> 它的样本量在哪里被看到。新增百分比面板必须在这里登记，否则校验失败。
SAMPLED_RATE_PANELS = {
    63: "77 诚实度表的「确认抢先条数」行",
    79: "77 诚实度表的「转述忠于原文」行",
}
# 不是抽样得出的比率(账单、进度、全量占比这类精确值)，不需要样本量。
# 53 各域丢弃占比 = 24h 全量 discarded/all_bench，不是抽样估计。
EXACT_RATIO_PANELS = {78}

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

    panels = dashboard.get("panels", [])

    def find_legacy_fixed_color(value, path: str = "dashboard") -> list[str]:
        found: list[str] = []
        if isinstance(value, dict):
            if value.get("mode") == "fixedColor":
                found.append(path)
            for key, child in value.items():
                found.extend(find_legacy_fixed_color(child, f"{path}.{key}"))
        elif isinstance(value, list):
            for index, child in enumerate(value):
                found.extend(find_legacy_fixed_color(child, f"{path}[{index}]"))
        return found

    legacy_fixed_color_paths = find_legacy_fixed_color(dashboard)
    if legacy_fixed_color_paths:
        errors.append(
            "Grafana color mode must be 'fixed', not unsupported 'fixedColor': "
            + ", ".join(legacy_fixed_color_paths)
        )

    ids = [p.get("id") for p in panels]
    dupes = {i for i in ids if ids.count(i) > 1}
    if dupes:
        errors.append(f"duplicate panel ids: {sorted(dupes)}")

    rects = []
    for panel in panels:
        g = panel.get("gridPos") or {}
        if not all(k in g for k in ("x", "y", "w", "h")):
            errors.append(f"panel {panel.get('id')} has invalid gridPos: {g}")
            continue
        rects.append((panel.get("id"), g["x"], g["y"], g["w"], g["h"]))
    for i, (id_a, ax, ay, aw, ah) in enumerate(rects):
        for id_b, bx, by, bw, bh in rects[i + 1:]:
            if ax < bx + bw and bx < ax + aw and ay < by + bh and by < ay + ah:
                errors.append(f"gridPos overlap between panels {id_a} and {id_b}")

    # 面板预算闸: 设计文档「怎么改」规定净增为零、目标上限 25(2026-07-08 现状 29)。
    # 超过现状即为净增, 必须先删一个再加一个。
    content_count = sum(1 for p in panels if p.get("type") not in ("row", "text"))
    if content_count > 23:
        errors.append(
            f"content panel count {content_count} exceeds the 23 ratchet — the design doc"
            " requires net-zero additions; remove one before adding one")

    # 黑话闸(设计原则#1): 标题与图例不得裸出英文黑话。描述里已解释的术语不在此列。
    import re as _re
    banned = _re.compile(
        r"critical|SLA|canary|p9\d|kind=|wilson|置信区间|fail-closed|deferred|stratum|分层",
        _re.IGNORECASE)
    for panel in panels:
        title = panel.get("title") or ""
        if banned.search(title):
            errors.append(f"panel {panel.get('id')} title contains banned jargon: {title!r}")
        for target in panel.get("targets", []) or []:
            legend = target.get("legendFormat") or ""
            if banned.search(legend):
                errors.append(
                    f"panel {panel.get('id')} legend contains banned jargon: {legend!r}")

        if panel.get("type") == "table":
            for target in panel.get("targets", []) or []:
                if target.get("expr") and target.get("format") != "table":
                    errors.append(
                        f"table panel {panel.get('id')} must request Prometheus table format"
                    )

    # 诚实契约闸：百分比面板要么是精确比率，要么必须登记它的样本量在哪。
    for panel in panels:
        pid = panel.get("id")
        if panel.get("type") in ("row", "text"):
            continue
        unit = (panel.get("fieldConfig", {}).get("defaults", {}) or {}).get("unit")
        if unit != "percent" or pid in EXACT_RATIO_PANELS or pid in SAMPLED_RATE_PANELS:
            continue
        errors.append(
            f"panel {pid} shows a percentage but its sample size is nowhere on the"
            " board — register it in SAMPLED_RATE_PANELS (or EXACT_RATIO_PANELS if it"
            " is not a sampled estimate). A rate without its n cannot be read.")

    row_titles = [p.get("title") for p in panels if p.get("type") == "row"]
    for section in EXPECTED_SECTIONS:
        if section not in row_titles:
            errors.append(f"missing section row: {section}")

    panels_by_id = {panel.get("id"): panel for panel in dashboard.get("panels", [])}
    # 无数据 != 0 (2026-07-28 铁律)。原本盯的是 54 信号率, 该面板已并入 77;
    # 现在盯 73「今天说错话的条数」——止损线上显示 0 比显示错的数还危险。
    said_wrong = panels_by_id.get(73)
    said_wrong_mappings = (
        said_wrong.get("fieldConfig", {}).get("defaults", {}).get("mappings", [])
        if said_wrong
        else []
    )
    if not any(
        mapping.get("options", {}).get("-1", {}).get("text") == "等待质检"
        for mapping in said_wrong_mappings
    ):
        errors.append("panel 73 must render the -1 sentinel as 等待质检")

    newsapi_table = panels_by_id.get(64)
    newsapi_transforms = newsapi_table.get("transformations", []) if newsapi_table else []
    newsapi_excluded_fields = {
        name
        for transform in newsapi_transforms
        for name, excluded in transform.get("options", {}).get("excludeByName", {}).items()
        if excluded
    }
    missing_internal_fields = {"__name__", "instance", "job"} - newsapi_excluded_fields
    if missing_internal_fields:
        errors.append(
            "panel 64 must hide internal Prometheus fields: "
            + ", ".join(sorted(missing_internal_fields))
        )

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

    for panel_id in (80, 81, 64, 82):
        panel = panels_by_id.get(panel_id)
        if panel and panel.get("gridPos", {}).get("w") != 24:
            errors.append(
                f"panel {panel_id} must use full dashboard width so evidence stays readable"
            )

    first_source_panel = panels_by_id.get(81)
    first_source_transforms = (
        first_source_panel.get("transformations", []) if first_source_panel else []
    )
    first_source_excluded_fields = {
        name
        for transform in first_source_transforms
        for name, excluded in transform.get("options", {})
        .get("excludeByName", {})
        .items()
        if excluded
    }
    if first_source_panel and "action" not in first_source_excluded_fields:
        errors.append(
            "panel 81 must hide the internal English action diagnostic; "
            "owner-facing reason and linked evidence are sufficient"
        )

    source_health_panel = panels_by_id.get(82)
    source_health_transforms = (
        source_health_panel.get("transformations", []) if source_health_panel else []
    )
    source_health_excluded_fields = {
        name
        for transform in source_health_transforms
        for name, excluded in transform.get("options", {})
        .get("excludeByName", {})
        .items()
        if excluded
    }
    if source_health_panel and "detail" not in source_health_excluded_fields:
        errors.append(
            "panel 82 must hide the internal detail label; "
            "owner-facing issue, source, and last error provide the evidence"
        )

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
        if not any("source_latency" in expr for expr in exprs):
            errors.append(
                f"panel {panel_id} must count PGC-owned source_latency rows"
            )
        if any("source_feed_lag" in expr for expr in exprs):
            errors.append(
                f"panel {panel_id} must not mix upstream source_feed_lag into PGC-owned latency"
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
