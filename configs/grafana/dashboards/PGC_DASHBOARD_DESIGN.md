# PGC Dashboard Design — 设计思路与演变记录

> 给未来改 dashboard 的人。改之前先读完这一页。

## 北极星指标

我们发出信息 A，其一手源为 A'。如果 A' 不在我们发出的集合里，算一次**输**；
如果 A' 在且比对照媒体更早，算一次**赢**。

**首发胜率 = 赢 / (赢 + 输)**，只算确信配对（embedding cosine similarity >= 0.72）。

这是 Pascal 在 2026-06-22 确认的定义，对应 Prometheus 指标
`pgc_first_source_confident_win_rate`。

## Dashboard 的职责

这块 Grafana 看板（uid: `pgc-pipeline`）回答 **三个问题**：

1. **我们抢先了吗** — 胜率是多少、在上升吗
2. **为什么没赢** — 输在哪个领域、是没源还是慢了
3. **怎么补** — 哪些一手源坏了、采购 backlog 多大

底部有一个运维保底 row，回答第四个问题：
4. **系统还活着吗** — 24h 发布量、队列积压、日志

## 设计原则

- **北极星优先**：前三行全部围绕首发胜率，不放无关运维面板。
- **不超过 25 面板**：76 面板的教训 — 面板多了没人看，等于没有 dashboard。
  每加一个面板要能回答"看了之后我会做什么动作"，答不上来就不加。
- **运维放底部且可折叠**：保底但不抢焦点。不加 LLM 成本、FD 压力、
  stage latency 等工程细节 — 那些属于独立的 ops dashboard（如果需要的话）。
- **标题用问题句式**：面板标题应该是"首发胜率是多少"而不是
  "pgc_first_source_confident_win_rate"。让非工程人员也能读懂。
- **description 字段写操作指引**：比如"为 0 说明管道挂了"，不只是解释指标含义。

## 面板结构（2026-06-22 版本）

### Row 1: 核心结果 — 我们抢先了吗
| 面板 | Prometheus 指标 | 为什么在这 |
|------|----------------|-----------|
| 首发胜率（确信对决） | `pgc_first_source_confident_win_rate` | 北极星 |
| 首发率（全部事件） | `pgc_first_source_win_rate` | 含噪声的全量参考，用于和确信对决对比 |
| 真实对决数 | `pgc_first_source_confident_races` | 分母——太小说明评测无统计意义 |
| 领先中位数 | `pgc_first_source_lead_median_hours` | 赢的时候赢多少 |
| 滞后中位数 | `pgc_first_source_lag_median_hours` | 输的时候差多远——接近 0 说明快翻盘了 |
| 无一手源（待补） | `pgc_first_source_no_first_party` | 采购 backlog，每减一个 = 潜在新赢面 |
| 数据新鲜度 | `pgc_first_source_leaderboard_age_seconds / 3600` | 保证你看到的不是过期数据 |

### Row 2: 走势 — 是否在上升
- 确信对决胜率 + 全量胜率双线 timeseries
- 14 天滚动窗口，修复一手源后约 14 天逐步抬升

### Row 3: 按领域 / 为什么没赢
| 面板 | 指标 | 操作含义 |
|------|------|---------|
| 各领域首发率 | `pgc_first_source_win_rate_by_domain` | 低的领域 = 下一步补源方向 |
| 没赢的原因 | `pgc_first_source_losses_by_reason` | `no_first_party` = 补源，`first_party_slower` = 提速/换源 |

### Row 4: 一手源健康
| 面板 | 指标 | 操作含义 |
|------|------|---------|
| 坏掉的一手源 | `pgc_first_party_feeds_broken_count` | 越低越好，每修一个胜率就涨 |
| 坏源数趋势 | 同上 timeseries | 下降 = 修复进度 |
| 待修一手源 | `pgc_first_party_feed_broken` (table) | 直接是 TODO 清单 |

### Row 5: 运维保底
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
| 2026-06-22 当前 | 以确信对决胜率为北极星重建 + 运维保底 | **24** |

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

所有 `pgc_*` 指标定义在 `eigenflux-pgc` 仓库的 `rsspipe/metrics.py`。
Prometheus scrape 配置在本仓 `configs/prometheus/prometheus.yml`（job: `pgc-pipeline`，端口 9090）。
Datasource uid `pgc-prometheus` 是 `prometheus` 的兼容别名，指向同一个实例。
