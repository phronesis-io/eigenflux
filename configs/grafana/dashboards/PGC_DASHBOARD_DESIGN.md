# PGC Dashboard Design — 设计思路与演变记录

> 给未来改 dashboard 的人。改之前先读完这一页。

## 北极星指标

**事件延迟** = 从事件实际发生到我们首次抓取的时间（模型从文章内容提取事件时间 T_event，
用 indexed_at 作为抓取时间）。

**真实对决** = 模型确认速度对比有意义的配对数。LLM 读取两篇文章全文，判断：
- 是否同一事件（语义，非纯 embedding）
- 各自的信源类型（official/wire/aggregation/commentary…）
- 速度对比是否有意义（排除反应性报道、不同事件、时间倒挂等）

**真实胜率** = 我们的信源更快的对决 / 真实对决数。

对应 Prometheus 指标 `pgc_event_*`（前缀），在 `rsspipe/metrics.py` 定义，
由 `scripts/event_verdicts.py` 每小时生成。

## Dashboard 的职责

这块 Grafana 看板（uid: `pgc-pipeline`）回答 **四个问题**：

1. **事件延迟多少** — 中位数、胜率、赢输对比、数据年龄
2. **现在要看哪里** — action count、首发关注、active latency、canary、source SLA、Twitter runway、风险趋势、source drilldown
3. **趋势怎样** — 延迟和胜率时序线
4. **系统还活着吗** — 24h 发布量、队列积压、日志（底部运维保底）

## 设计原则

- **北极星优先**：核心结果仍在首屏；owner cockpit 紧跟其后，因为它回答当前行动面。
- **不超过 25 面板**：76 面板的教训 — 面板多了没人看，等于没有 dashboard。
  每加一个面板要能回答"看了之后我会做什么动作"，答不上来就不加。
- **运维放底部且可折叠**：保底但不抢焦点。
- **标题用问题句式**：面板标题应该是"赢/输"而不是 `pgc_event_real_wins`。
- **description 字段写操作指引**：比如"超过 2h 说明 timer 卡了"。

## 面板结构（2026-06-23 版本）

### Row 1: 核心结果 — 我们离事件有多近
| 面板 | Prometheus 指标 | 操作含义 |
|------|----------------|---------|
| 事件延迟中位数 | `pgc_event_latency_median_hours` | 北极星 — 越低越好 |
| 真实对决 | `pgc_event_meaningful_races` | 分母 — 太小说明评测无统计意义 |
| 真实胜率 | `pgc_event_real_win_rate` | 赢的比例 |
| 过滤配对 | `pgc_event_non_meaningful` | 模型过滤掉的不可比配对数 |
| 赢/输 | `pgc_event_real_wins` / `pgc_event_real_losses` | 绝对数 — 赢多输少 = 健康 |
| 判定数据年龄 | `pgc_event_verdicts_age_seconds / 3600` | 超 2h = timer 可能卡了 |

### Row 2: Owner Cockpit — 现在要看哪里
| 面板 | 指标 | 操作含义 |
|------|------|---------|
| 立即要处理几件事 | `pgc_first_source_audit_attention + pgc_source_health_canaries_failed + pgc_source_health_sla_attention + pgc_signal_latency_actionable_breaches_3h{source_tier=~"T0\|T1"}` | owner action queue |
| 首发关注 | `pgc_first_source_audit_attention` | benchmark/secondary items needing primary-source review |
| T0/T1 仍超时 | `pgc_signal_latency_actionable_breaches_3h{source_tier=~"T0\|T1"}` | active high-priority source latency breaches |
| Canary 失败 | `pgc_source_health_canaries_failed` | must-have source checks failing now |
| 源 SLA 关注 | `pgc_source_health_sla_attention` | registry-defined source-health SLA breaches |
| Twitter runway | `pgc_twitterapi_credits_days_to_empty` | paid X/Twitter source budget runway |
| 风险趋势 | owner-action components over time | whether active risk is improving or worsening |
| Active source drilldown | `pgc_signal_latency_active_source_breaches_3h{kind=~"source_latency\|source_feed_lag"}` | source/stage/tier/kind for current offenders |

### Row 3: 走势 — 是否在上升
- 事件延迟 + 真实胜率双线 timeseries（`pgc_event_latency_median_hours`, `pgc_event_real_win_rate`）

### Row 4: 运维保底
| 面板 | 指标 | 操作含义 |
|------|------|---------|
| 近 24h 发布数 | `increase(pgc_published_total[24h])` | 为 0 = 管道挂了 |
| 队列积压 | `pgc_queue_*` sum | 持续增长 = 某阶段卡住 |
| Worker 最大空闲 | `max(pgc_worker_last_run_age_seconds)` | 超 600s = worker 可能死了 |
| 发布量趋势 | `increase(pgc_published_total[1h])` 柱状图 | 直观看吞吐节奏 |
| Pipeline 日志 | Loki `{service="pgc-pipeline"}` | 排障用 |

## 演变记录

| 日期 | 事件 | 面板数 |
|------|------|--------|
| 2026-05-27 | 初版，Loki-based | ~5 |
| 2026-05-28~29 | 切 Prometheus，加 stage latency / per-source funnel | ~27 |
| 2026-06-03~04 | Ecosystem self-documenting, supply/demand | ~35 |
| 2026-06-16~19 | 大量运维面板：SLA/Canary/Twitter credit/event timeline | **76** |
| 2026-06-20 | 砍到一块板，plain-language titles | 减少 |
| 2026-06-21 `8043731` | Pascal 要求纯基准看板，重建为 16 面板 | **16** |
| 2026-06-22 `ec15eda` | 意外恢复 76 面板（误判删减为丢失） | **76** |
| 2026-06-22 | 以确信对决胜率为北极星重建 | **24** |
| 2026-06-23 `329b47c` | 全面迁移至模型判定 (pgc_event_*)，删除所有旧 pgc_first_source_* 面板 | **22** |
| 2026-06-23 | 加 owner cockpit，删除低优先级延迟分布和旧坏源面板 | **25** |
| 2026-06-29 | 迁移至价值(信号)为顶 + 覆盖/准确/速度护栏 (北极星重定义) | ~30 |
| 2026-06-30 当前 | 清理：删 8 个空的双语 row(总览/Owner Cockpit 等历史遗留空壳); 修护栏区两处面板重叠(51/58、7/60 共占 x16); 各域召回→各域丢弃占比(召回排除丢弃后恒≈100%, 无信号); 两个 Deep Dive row 改名区分(诊断·对标 / 工程诊断·信号延迟) | **44** (含 4 row+text) |

**教训**：76 面板之所以出现，是因为每次有新指标就加面板，没人问"看了会做什么"。
回退之所以发生，是因为另一个 session 看到"面板少了"就以为是 bug。
写这份文档就是为了断掉这个循环——改 dashboard 之前先读设计原则。

## 怎么改这个 Dashboard

1. 先读完本文档的设计原则。
2. 新面板必须回答"看了之后我会做什么动作"。
3. 总面板数不超过 25。要加就先砍一个。
4. 改完跑 `python3 -c "import json; json.load(open('pgc-pipeline.json'))"` 验证 JSON。
5. 推到 main 后，SSH 到 aliapmo 执行 `cd /data/git/eigenflux && git pull && docker compose -f docker-compose.monitor.yml restart grafana`。
6. 更新本文档的面板结构表和演变记录。

## 指标来源

所有 `pgc_event_*` 指标定义在 `eigenflux-pgc` 仓库的 `rsspipe/metrics.py`。
数据由 `scripts/event_verdicts.py` 每小时生成，写入 `event_verdicts.json`。
`rsspipe/viewer/prometheus.py` 的 `refresh_event_verdicts()` 读取该 JSON 并更新 Gauges。

Prometheus scrape 配置在本仓 `configs/prometheus/prometheus.yml`（job: `pgc-pipeline`，端口 9090）。
Datasource uid `pgc-prometheus` 指向 `pgc-prometheus` Docker 容器（端口 9091）。

## 已退役的指标（2026-06-23）

以下旧 leaderboard 指标不再有 dashboard 面板消费，但 Gauge 定义仍在 `metrics.py` 中（避免 scrape error）：
- `pgc_first_source_confident_win_rate` — 旧北极星
- `pgc_first_source_win_rate` / `pgc_first_source_win_rate_by_domain`
- `pgc_first_source_losses_by_reason`
- `pgc_first_source_no_first_party`
- `pgc_first_source_leaderboard_age_seconds`
- `pgc_first_source_lead_median_hours` / `lag_median_hours`
- `pgc_first_source_confident_races`

> **2026-06-30 校正**：旧版本曾称 `pgc-first-source-leaderboard.timer` 已 disable —— 这是**错误**的。
> prod 上该 timer 仍 `enabled`+`active`(每日 10:30 CST)，因为 `pgc_first_source_speed_win_share`
> (首发走势面板的"抢先率"线)仍由它产出的 leaderboard 报告驱动。改前以
> `ssh aliap systemctl list-timers 'pgc-*'` 实际状态为准。
