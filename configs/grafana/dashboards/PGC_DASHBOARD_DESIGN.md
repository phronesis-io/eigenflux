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

- **北极星优先**：价值(信号)在首屏顶层；三护栏紧随；诊断与运维依次下沉。
- **段落对齐 NORTH_STAR 四轴**：价值 → 护栏(覆盖·准确·速度) → 诊断(降级) → 运维(各自独立)。
- **控制在 ~35 面板内**：76 面板的教训 — 面板多了没人看，等于没有 dashboard。
  每加一个面板要能回答"看了之后我会做什么动作"，答不上来就不加。
- **运维放底部且可折叠**：保底但不抢焦点。
- **标题用问题句式**：面板标题应该是"赢/输"而不是 `pgc_event_real_wins`。
- **description 字段写操作指引**：比如"超过 2h 说明 timer 卡了"。

## 面板结构（2026-06-30 版本 — 对齐 `docs/NORTH_STAR.md` 四段式）

布局直接镜像 NORTH_STAR：**价值(顶层) → 三护栏 → 诊断(降级) → 运维(各自独立)**。
35 个面板对象（含 5 个 row/text 分隔）。每段一个 row 标题。

### 🎯 价值(信号) — 顶层目标
| 面板 | Prometheus 指标 | 操作含义 |
|------|----------------|---------|
| 信号率 (价值·顶层) | `pgc_broadcast_signal_rate` | 北极星之顶 — 广播是信号还是噪声; <85 黄 <60 红 |
| 噪声泄漏 (按类型) | `pgc_broadcast_noise_by_type` | 哪类噪声漏过门(体育/本地琐事领头) |
| 错分类 (域填错) | `pgc_broadcast_miscategorized` | 域被 LLM 填错的条数; 越低越好 |

### 🛡️ 护栏 — 覆盖 · 准确 · 速度（三条须各自绿）
| 面板 | 指标 | 操作含义 |
|------|------|---------|
| 护栏·捕获召回(管线) | `pgc_coverage_recall_pct` | 决定处理的 benchmark 项中链路成功交付比例; 低=抓取/解析坏 |
| 护栏·忠实率(准确) | `pgc_broadcast_faithfulness_pct` | 抽样广播忠于原文比例; 目标≥90 |
| 护栏·首源入库(速度) | `pgc_first_party_ingest_median_minutes` | 一手源发布→入库 p50 分钟; 目标≤5 |
| 护栏·错丢真损率(门) | `pgc_discard_false_loss_rate` | 门错杀真事件比例(真·过度丢弃); 低=好 |
| 各域捕获召回率 | `pgc_coverage_recall_by_domain` | 低域=该域付费墙/抓取坏(law) |
| 信源可信度构成 | `pgc_broadcast_reliability_share_pct` | 按可信度标签占比(决策#3); unverified+low 升=信任风险 |
| 护栏·低可信占比 | `pgc_broadcast_low_trust_pct` | unverified+low 信源广播占比; 低=好 |

### 📉 诊断 — 对标媒体 / 首发胜率（已降级, 非北极星）
| 面板 | 指标 | 操作含义 |
|------|------|---------|
| 首发判定准确率 (诚实) | `pgc_event_win_precision` | 声称的赢里真同事件比例; 真信任数 |
| 对决胜率 (诊断) | `pgc_event_real_win_rate` | 已假设对决为真的自我打分; 非信任指标 |
| 真实对决 | `pgc_event_meaningful_races` | 分母 — 太小则评测无统计意义 |
| 赢/输 | `pgc_event_real_wins` / `pgc_event_real_losses` | 绝对数 |
| 判定数据年龄 | `pgc_event_verdicts_age_seconds / 3600` | 超 2h = timer 可能卡了 |
| 首发走势 | `pgc_event_latency_*` / `_real_win_rate` / `_win_precision` / `first_source_speed_win_share` | 准确率·抢先率·延迟时序 |

### 🔧 运维 — 系统健康（各项独立, 永不合成单一待办数）
| 面板 | 指标 | 操作含义 |
|------|------|---------|
| 近 24h 发布数 | `pgc_items_24h{status="published"}` | 为 0 = 管道挂了 (reset-safe, 非 increase()) |
| 队列积压 | `sum(pgc_queue_*)` | 持续增长 = 某阶段卡住 |
| Worker 最大空闲 | `max(pgc_worker_last_run_age_seconds)` | 超 600s = worker 可能死了 |
| Twitter / NewsAPI / ScraperAPI 用量 | `pgc_twitterapi_credits_days_to_empty` / `pgc_newsapi_tokens_pct` / `pgc_scraperapi_credits_pct` | 付费源预算跑道 |
| 我方管线延迟 | `sum(pgc_signal_latency_active_source_breaches_3h{source_tier=~"T0\|T1",kind="source_latency"})` | T0/T1 抓取超时(修代码); 0=健康 |
| Canary 失败 / 源 SLA 关注 / 首发关注 | `pgc_source_health_canaries_failed` / `_sla_attention` / `pgc_first_source_audit_attention` | 各自独立的关注项 |
| 发布量趋势 | `pgc_items_24h/24` + `pgc_items_1h` | 24h 均值线 + 实时滑窗(均 reset-safe) |
| 各源延迟明细 | `pgc_signal_latency_active_source_breaches_3h{kind=~"source_latency\|source_feed_lag"}` | 谁在拖延迟, 按源+类型拆 |
| 运维行动项走势 | 5 条独立分量(首发关注/T0·T1超时/Canary/关键源/源SLA) | **按 NORTH_STAR 约定不再合成"总行动数"** |
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
| 2026-06-29 | 迁移至价值(信号)为顶 + 覆盖/准确/速度护栏 (北极星重定义) | ~34 |
| 2026-06-30 当前 | 按 `NORTH_STAR.md` 四段式重排(价值→护栏→诊断→运维); 删 `风险趋势` 的合成"总行动数"线(违背"不合成"约定); 校正下方两处过时声明 | **35** |

**教训**：76 面板之所以出现，是因为每次有新指标就加面板，没人问"看了会做什么"。
回退之所以发生，是因为另一个 session 看到"面板少了"就以为是 bug。
写这份文档就是为了断掉这个循环——改 dashboard 之前先读设计原则。

## 怎么改这个 Dashboard

1. 先读完本文档的设计原则。
2. 新面板必须回答"看了之后我会做什么动作"。
3. 总面板数控制在 ~35 以内（含 row/text 分隔）。要加就先砍一个。每段对齐 NORTH_STAR 四轴, 不要把诊断/运维面板塞回价值/护栏段。
4. 改完跑 `python3 -c "import json; json.load(open('pgc-pipeline.json'))"` 验证 JSON。
5. 推到 main 后，SSH 到 aliapmo 执行 `cd /data/git/eigenflux && git pull && docker compose -f docker-compose.monitor.yml restart grafana`。
6. 更新本文档的面板结构表和演变记录。

## 指标来源

所有 `pgc_event_*` 指标定义在 `eigenflux-pgc` 仓库的 `rsspipe/metrics.py`。
数据由 `scripts/event_verdicts.py` 每小时生成，写入 `event_verdicts.json`。
`rsspipe/viewer/prometheus.py` 的 `refresh_event_verdicts()` 读取该 JSON 并更新 Gauges。

Prometheus scrape 配置在本仓 `configs/prometheus/prometheus.yml`（job: `pgc-pipeline`，端口 9090）。
Datasource uid `pgc-prometheus` 指向 `pgc-prometheus` Docker 容器（端口 9091）。

## 已退役的指标

以下旧 leaderboard 指标不再有 dashboard 面板消费，但 Gauge 定义仍在 `metrics.py` 中（避免 scrape error）：
- `pgc_first_source_confident_win_rate` — 旧北极星
- `pgc_first_source_win_rate` / `pgc_first_source_win_rate_by_domain`
- `pgc_first_source_losses_by_reason`
- `pgc_first_source_no_first_party`
- `pgc_first_source_leaderboard_age_seconds`
- `pgc_first_source_lead_median_hours` / `lag_median_hours`
- `pgc_first_source_confident_races`

> **2026-06-30 校正**：旧版本曾写"`pgc-first-source-leaderboard.timer` 已 disable"。
> 这是**过时/错误**的——prod 上该 timer 仍 `enabled` + `active`（每日 10:30 CST 运行），
> 因为 `pgc_first_source_speed_win_share`（首发走势面板 #9 的"抢先率"线）仍由
> `refresh_first_source_leaderboard()` 从 leaderboard 报告读取。改 dashboard 前请以
> `ssh aliap systemctl list-timers 'pgc-*'` 的实际状态为准，勿信本文档的历史断言。

> **`pgc_first_source_audit_attention`（运维·首发关注）的真实数据来源**：不是独立 timer，
> 而是 `refresh_first_source_audit_report()` **优先读每日 source-health 报告内嵌的 audit**
> （`pgc-source-health-report.timer` 写），standalone `data/first_source_audit/` 仅作人工回放兜底。
> 报告缺失时 attention/critical/warn 置 **NaN**（面板显示 No-data），而非 0（避免假绿）。
