# PGC Grafana Dashboard PRD

Date: 2026-06-16
Owner: PGC / EigenFlux operations
Status: implemented in `configs/grafana/dashboards/pgc-pipeline.json`
Design reference: `configs/grafana/dashboards/user-growth.json`
Revision: 2026-06-17, first-source audit metrics added to the first screen.

## Problem

The PGC Grafana dashboard had data, but it was not effective as a professional
operations surface. It mixed lifetime counters, live health, source quality,
cost, and logs without a clear incident-response path. Operators could see
numbers, but had to infer whether the system was healthy, where a bottleneck
was, and which source or stage needed attention.

This became painful during recent PGC incidents where a critical source was
alive upstream but not deliverable downstream. The dashboard must make similar
failures obvious without requiring ad hoc SQL or shell inspection.

The newer first-source incidents are more specific: a benchmark or secondary
source can already contain a high-value signal, while PGC either lacks the
right primary source, sees the primary source too late, or cannot classify the
gap confidently. The dashboard therefore needs a first-screen audit surface, not
only generic crawler/source-health charts.

## Goals

- Match the readability of the User Growth dashboard: compact KPI row, clear
  Chinese business labels, large trend panels, and table-first detail views.
- Give an operator a 30-second command-center view of PGC health.
- Make first-source misses, late primary-source sightings, and benchmark-only
  discoveries visible immediately.
- Separate delivery, source health, quality/cost, diagnostics, and logs.
- Prefer rates, rolling windows, and ratios over raw lifetime totals when the
  question is operational.
- Keep drilldown panels close to the summary metric they explain.
- Use the stable Grafana datasource UID `pgc-prometheus` for PGC Prometheus
  panels and `loki` for logs.
- Keep the dashboard provisionable from JSON and testable through Grafana's API.

## Non-Goals

- This PRD consumes the first-source audit Prometheus metrics exported by PGC;
  it does not define the audit algorithm itself.
- This PRD does not replace Lark/webhook canaries; Grafana is the operator
  cockpit, while webhooks remain the push-alert surface.
- This PRD does not create alert rules. Alerting can be layered on top once the
  dashboard panels settle.

## Users

- On-call engineer: needs to know whether PGC is stuck, degraded, or merely
  noisy.
- Product/operator: needs to see whether important sources and topics are being
  delivered.
- Backend engineer: needs to isolate whether a problem is crawl, extract, LLM,
  publish, source inventory, or external API budget.

## Dashboard Structure

1. 一手信号审计 / First-Source Audit
   - 需处理的一手信号
   - Critical 漏配/滞后
   - 审计报告新鲜度
   - 已审计 benchmark
   - 一手审计趋势
   - 审计判定分布
   - 严重度分布
   - 信源库覆盖规模

2. 信源可靠性 / Source Reliability
   - Canary 失败
   - 关键源需处理
   - 健康报告新鲜度
   - 活跃源覆盖率
   - Source Health 趋势
   - Canary 明细
   - 当前 blocked sources
   - 即将被 block 的来源

3. 内容交付 / Delivery
   - 近1小时发布
   - 待处理队列
   - 当前阻塞源
   - NewsAPI 用量
   - 发布趋势、队列分布、24小时内容状态
   - 来源发布榜、异常来源榜

4. 来源健康 / Source Health
   - 失败源、worker 心跳、LLM 失败、FD 压力
   - 来源转化率、高热来源

5. 质量与成本 / Quality & Cost
   - LLM 调用结果、LLM 延迟、token 消耗
   - Signal Gate、端到端发布延迟

6. 工程诊断 / Diagnostics
   - Worker 心跳、阶段耗时、错误压力
   - PGC pipeline Loki stream

## Acceptance Criteria

- Grafana dashboard loads with no "data source not found" errors.
- Every Prometheus panel uses `uid=pgc-prometheus`.
- Loki log panel uses `uid=loki`.
- First-source audit panels query `pgc_first_source_audit_*` metrics and return
  non-empty frames in production.
- Representative panel queries return non-empty frames through Grafana API.
- Dashboard JSON is valid, provisionable, and committed to git.
- `scripts/local/validate_pgc_grafana_dashboard.py` passes static validation and
  the production Prometheus query sweep.
- Production checkout is clean after deployment except ignored operational
  backups.

## Follow-Ups

- Add Grafana alert rules for max worker age, queue depth, LLM error spikes,
  blocked-feed spikes, and first-source critical spikes.
- Add a topic coverage panel once demand-canary metrics are exported.
