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
Revision: 2026-06-19, added a first-screen owner cockpit modeled after User Growth: one KPI row plus trend/drilldown panels answering whether PGC is missing signals, slow, source-unhealthy, or close to external API budget limits.
Revision: 2026-07-24, owner-language redesign: the first screen now asks what
needs action; value/coverage/correctness/speed are peer product outcomes; the
NewsAPI topic panel shows cursor freshness instead of token burn so healthy
HTTP calls cannot hide day-old coverage.
Revision: 2026-07-31, production visual acceptance and owner-comprehension
redesign: separate estimated wrong-item count from faithfulness percentage;
separate PGC-owned latency from upstream publication lag; replace opaque totals
with source-level and article-level evidence tables; expose current source
inventory; remove raw logs and duplicate trend panels from the owner surface.

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
- Separate delivery, source health, quality, speed, and paid capacity.
- Prefer rates, rolling windows, and ratios over raw lifetime totals when the
  question is operational.
- Keep drilldown panels close to the summary metric they explain.
- Use the stable Grafana datasource UID `pgc-prometheus` for every dashboard
  panel.
- Keep the dashboard provisionable from JSON and testable through Grafana's API.

## Non-Goals

- This PRD consumes the first-source audit Prometheus metrics exported by PGC;
  it does not define the audit algorithm itself.
- This PRD does not replace Lark/webhook canaries; Grafana is the operator
  cockpit, while webhooks remain the push-alert surface.
- This PRD does not create alert rules. Alerting can be layered on top once the
  dashboard panels settle.
- Raw logs remain available to engineers in Loki, but do not occupy the owner
  dashboard. A user should never need to read logs to understand a red number.

## Users

- Owner / executive: needs each panel to answer a concrete product question,
  with a named source or article behind every action count.
- Product/operator: needs to see whether important sources and topics are being
  delivered.
- On-call engineer: needs to know whether PGC is stuck, degraded, or merely
  observing upstream publication lag.
- Backend engineer: needs to isolate whether a problem is crawl, extract, LLM,
  publish, source inventory, or external API budget.

## Dashboard Structure

1. 结果 — 今天做成了什么
   - 今天发出了多少条信号
   - 今天确认抢先了多少条
   - 抢先时通常早了多久
   - 我们比媒体更早的比例

2. 质量 — 今天有没有伤害用户
   - 可能转述不准的条数（明确标为估计）
   - 转述忠实率（明确显示百分号）
   - 质检有没有停

3. 行动 — 现在具体要处理什么
   - PGC 自己能修的故障数
   - 关键来源断了几个
   - PGC 自己造成的迟到
   - 一手来源空缺线索
   - 哪些来源正在拖慢：来源名、优先级、PGC 慢或上游晚发、迟到条数
   - 哪篇报道暴露一手来源空缺：标题、原文、原因、建议动作

4. 漏文 — 到底漏了什么，为什么
   - 过去 24 小时的损失按原因拆分
   - 第一判官疑似、两家确认真漏、二判否决、不可用、未完成分别显示

5. 可信度 — 这些数字能不能信
   - 指标值、样本量、误差范围、没判成数量、未覆盖人群同表展示

6. 趋势 — 最近有没有变好
   - 抢先率和延迟放在一张趋势图中，避免重复趋势面板

7. 运行 — 信源、积压与额度是否健康
   - 是否持续积压
   - 配置、运行、被封、高风险失败、超时需处理、关键源观察的来源数
   - Twitter 额度还能使用多久
   - NewsAPI 与网页代理本月额度
   - NewsAPI 每个主题是否新鲜
   - 当前有问题的具体来源、问题类型、证据、最近错误和来源地址

## Acceptance Criteria

- Grafana dashboard loads with no "data source not found" errors.
- Every Prometheus panel uses `uid=pgc-prometheus`.
- The owner dashboard contains no raw log panel and no opaque total whose
  components cannot be explained nearby.
- `PGC 自己造成的迟到` only counts `kind="source_latency"`; it must not mix
  upstream `source_feed_lag` into a PGC fault.
- `哪些来源正在拖慢` includes both `source_latency` and `source_feed_lag` and
  exposes source name, priority, reason, and three-hour count.
- `哪篇报道暴露了一手来源空缺` queries
  `pgc_first_source_audit_attention_item_info` and exposes title, original URL,
  benchmark source, category, reason, severity, and action.
- The first-source detail metric is bounded to at most 25 current attention
  records and clears old label sets on every refresh.
- Estimated wrong-item count and faithfulness percentage are separate panels;
  the count is explicitly labeled as estimated and the rate uses a percent unit.
- Every sampled percentage identifies its sample-size row in the trust table.
- Source status shows configured, active, blocked, high-failure, SLA-attention,
  and critical-watch counts rather than worker internals.
- `具体哪些信源有问题` queries
  `pgc_source_health_problem_source_info`, is bounded to 50 current rows, and
  clears stale rows whenever the source-health report changes or disappears.
- The discard dual-review panel colors zero confirmed losses green, suspected
  candidates yellow, vetoes blue, and any unavailable or unfinished review as
  yellow/red.
- Representative panel queries return non-empty frames through Grafana API.
- Dashboard JSON is valid, provisionable, and committed to git.
- `scripts/local/validate_pgc_grafana_dashboard.py` passes static validation and
  the production Prometheus query sweep.
- Production checkout is clean after deployment except ignored operational
  backups.
- Prometheus table panels request `format=table`, so source/topic labels render
  as owner-readable columns rather than raw series strings.
- Missing quality-review data renders as `等待质检`; the internal `-1` sentinel
  must never be shown as a negative product score.

## Follow-Ups

- Add Grafana alert rules for max worker age, queue depth, LLM error spikes,
  blocked-feed spikes, and first-source critical spikes.
- Add a topic coverage panel once demand-canary metrics are exported.
