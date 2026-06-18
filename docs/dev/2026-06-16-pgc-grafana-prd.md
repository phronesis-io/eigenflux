# PGC Grafana Dashboard PRD

Date: 2026-06-16
Owner: PGC / EigenFlux operations
Status: implemented in `configs/grafana/dashboards/pgc-pipeline.json`
Design reference: `configs/grafana/dashboards/user-growth.json`
Revision: 2026-06-17, first-source audit metrics added to the first screen.
Revision: 2026-06-18, low-latency signal-network SLI panels added between first-source audit and event timeline; panel titles rewritten as user/operator questions.
Revision: 2026-06-18, SLA breach panels switched to actionable latency breaches so operators see real low-latency failures before raw timestamp/noise diagnostics.
Revision: 2026-06-18, first-screen SLA breach panels switched to 3h active actionable breaches so operators can distinguish current incidents from 24h residual debt.
Revision: 2026-06-18, latency breach-kind panel added so operators can tell true source latency from recovery backfill, date-only timestamps, and non-signal statuses.
Revision: 2026-06-18, source reliability panels now expose `pgc_source_health_sla_attention` so registry-defined per-source SLA failures are visible beside canary and critical-source failures.
Revision: 2026-06-18, source reliability now includes a per-source SLA offender table backed by `pgc_source_health_sla_attention_source`.

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
- Make world-to-PGC and world-to-push latency visible by source class and tier,
  so delayed signals are investigated before users discover them.
- Make source-specific registry SLA failures visible as a first-class operator
  signal, not only as JSON/webhook detail.
- Put active actionable latency failures in the first visible SLA panels, while
  keeping 24h and raw breach counters available as review/diagnostic evidence.
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

1. 一手有没有漏 / First-Source Coverage
   - 今天有多少一手问题要处理
   - 严重漏配/晚到有多少
   - 审计刚刚跑过吗
   - 今天审了多少 benchmark
   - 一手问题是在变多还是变少
   - 问题属于哪种类型
   - 问题严重到什么程度
   - 一手源库覆盖够不够

2. 信号够不够快 / Signal Latency
   - 现在高优先级信号还在超时吗
   - 官方源多久进入 PGC
   - 机器源是否保持低延迟
   - 高优先级信号多久发出去
   - 哪类源最晚被我们看到
   - 哪类源最晚发给下游
   - 哪些类别需要马上处理
   - 这些超时是事故还是回补噪音
   - 当前哪些信源正在拖慢

3. 每条信号卡在哪一跳 / Event Timeline
   - 链路事实还在写入吗
   - 24h 写了多少链路事件
   - 24h 有多少内容可复盘
   - 24h 有多少推送证据
   - 链路证据是否稳定增长
   - 慢/断在哪个阶段

4. 信源是否可靠 / Source Reliability
   - Canary 有没有失败
   - 关键源是否需要处理
   - 信源 SLA 是否破线
   - 健康报告刚刚跑过吗
   - 活跃源覆盖率够不够
   - 源健康是在变好还是变差
   - 哪些源违反 SLA
   - 哪些 Canary 失败
   - 哪些源被 block
   - 哪些源快被 block

5. 内容有没有送达 / Delivery
   - 近 1 小时发出多少
   - 队列是否积压
   - 当前有多少阻塞源
   - NewsAPI 预算是否安全
   - TwitterAPI 还能撑多久
   - 外部 API 检查是否异常
   - 发布量是否稳定
   - 队列卡在哪个状态
   - 24h 内容状态分布
   - 哪些来源贡献最多
   - 哪些来源正在异常

6. 生产链路是否健康 / Pipeline Health
   - 哪些源连续失败
   - Worker 是否卡住
   - LLM 是否在报错
   - FD 是否有压力
   - 来源转化是否健康
   - 哪些来源最热

7. 质量和成本是否失控 / Quality & Cost
   - LLM 调用是否稳定
   - LLM p95 是否变慢
   - Token 成本是否异常
   - Signal Gate 是否过严
   - 端到端发布是否超时

8. 工程诊断 / Deep Dive
   - Worker 心跳明细
   - 各阶段耗时
   - 错误压力是否升高
   - Pipeline 日志

## Acceptance Criteria

- Grafana dashboard loads with no "data source not found" errors.
- Every Prometheus panel uses `uid=pgc-prometheus`.
- Loki log panel uses `uid=loki`.
- First-source audit panels query `pgc_first_source_audit_*` metrics and return
  non-empty frames in production.
- Low-latency panels query `pgc_signal_latency_*` metrics and return production
  data where appropriate; first-screen SLA breach panels use
  `pgc_signal_latency_actionable_breaches_3h`, while 24h actionable/raw breach
  metrics remain available for review. The active SLA breach table is allowed
  to be empty when no class/tier has current actionable breaches.
- The latency breach-kind panel queries `pgc_signal_latency_breach_kind_24h` so
  operators can explain why raw SLA debt is not always an active first-source
  incident.
- The active source latency panel queries
  `pgc_signal_latency_active_source_breaches_3h{kind=~"source_latency|source_feed_lag"}`,
  so an owner can see the exact source names currently dragging the
  low-latency promise instead of stopping at class/tier aggregates. The `kind`
  label distinguishes PGC/processing/polling latency from upstream RSS feed lag;
  non-actionable active reasons remain visible in the adjacent breach-kind
  panel.
- Source reliability includes a stat panel for
  `pgc_source_health_sla_attention{job="pgc-pipeline"}` and the source-health
  trend panel includes the same series, so registry-defined poll-gap, quiet, and
  blocked-source SLA failures are visible in both current-state and historical
  views.
- The source SLA drilldown table queries
  `pgc_source_health_sla_attention_source{job="pgc-pipeline"}` and is allowed
  to be empty in the healthy state; when non-empty it must expose source name,
  category, source type/class/tier, stable reason, and critical label.
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
