# PGC Dashboard Design — 设计思路与演变记录

> 给未来改 dashboard 的人。改之前先读完这一页。

## 北极星指标

**北极星 = 信号价值**（2026-06-29 重定义）：广播的是否是对接收方有价值的信号（面板 54 信号率），
覆盖 / 准确 / 速度是三条护栏。以下为速度轴与对决口径的指标定义：

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

1. **北极星与护栏怎么样** — 信号率、覆盖 / 准确 / 速度、赢输对比、数据年龄
2. **现在要看哪里** — 待处理故障、观察清单、首发关注、活跃延迟、额度余量、风险趋势、来源明细
3. **趋势怎样** — 延迟和胜率时序线
4. **系统还活着吗** — 24h 发布量、队列积压、日志（底部运维保底）

## 设计原则

- **北极星优先**：核心结果仍在首屏；owner cockpit 紧跟其后，因为它回答当前行动面。
- **不超过 25 面板**：76 面板的教训 — 面板多了没人看，等于没有 dashboard。
  每加一个面板要能回答"看了之后我会做什么动作"，答不上来就不加。
- **运维放底部且可折叠**：保底但不抢焦点。
- **标题用问题句式**：面板标题应该是"赢/输"而不是 `pgc_event_real_wins`。
- **description 字段写操作指引**：比如"超过 2h 说明 timer 卡了"。
- **黑话默认判失败，原始标签表禁止裸奔**：标题/图例/表格列如果要查文档才看懂（`SLA`、`canary`、`kind=xxx` 这类内部黑话/枚举），或者表格直接吐 Prometheus 原始多列标签（`source_class`/`source_tier`/`instance`/`job`…），对 Pascal 来说就是噪声，不管 description 写得多详细都没用——他不会去读 hover。要么把标签转译成人话（标题、图例都要翻），要么直接砍掉，别留"技术上对但没人能读"的面板。

## 2026-07-02 口径切换：榜单声称改为大模型确认制（metric v2）

榜单的"抢先"声称从"相似度够高就算"改为"大模型读两篇全文确认同一事件、
且我们确实更早才算"；没判完的事件标 pending、不进分母。对应
eigenflux-pgc `docs/plans/2026-07-01-llm-verdict-authority.md`。

对看板的影响：
- `pgc_first_source_win_rate` 跨 2026-07-02 不可比（ledger 里有 metric_version:2 标记）。
- `pgc_event_win_precision` 降级为纯诊断（旧判断器准确率），不再代表对外口径
  （面板 57 标题/说明已改）。
- 新指标可用：`pgc_first_source_metric_version`、`pgc_first_source_pending_verdicts`、
  `pgc_event_shadow_llm_win_claims`、`pgc_event_gate_won_llm_rejected`、
  `pgc_event_gate_missed_llm_won`（暂未上面板，按"看了会做什么"原则先不加）。

## 面板结构（2026-07-08 版本）

内容面板 29 个（另有 3 个 row 分隔 + 1 个 text 头部说明）。

### Row 北极星 — 信号价值 · 护栏（覆盖 / 准确 / 速度）
| 面板 (id) | Prometheus 指标 | 操作含义 |
|------|----------------|---------|
| 信号率（北极星）(54) | signal quality | 北极星顶层 — 广播里真信号占比 |
| 错分类率 (56) | misclassification | 域填错% — 高了修分类 |
| 护栏·捕获召回 (50) | coverage recall | 管线是否漏抓大事件 |
| 护栏 · 忠实率 (52) | faithfulness | 广播是否忠于原文 |
| 护栏 · 首源入库速度 (51) | event latency | 事件发生→入库多快 |
| 护栏 · 低可信来源占比 (58) | broadcast reliability | 低可信源广播占比 |
| 有效对决数 (5) | `pgc_event_meaningful_races` | 评测分母，太小=无统计意义 |
| 赢/输 (19) | `pgc_event_real_wins/losses` | 绝对数 |
| 判定数据年龄 (7) | `pgc_event_verdicts_age_seconds` | 超 2h = timer 卡了 |
| 诊断 · 丢弃质量 (60) | 错丢率（基准分层 / 全池）| 软信号：门是否错杀真事件，两线同升才是闸门问题 |
| 首发走势 — 抢先率与延迟 (9) | latency / 胜率 / 抢先率 | 趋势；**7/2 有口径断点标注** |
| 各领域丢弃占比 (53) | discard by domain | 哪个域被门丢得多 |

### Row 运维 — 系统健康
| 面板 (id) | 指标 | 操作含义 |
|------|------|---------|
| 近 24h 发布数 (21) | published 24h | 为 0 = 管道挂了 |
| 队列积压 (22) | queue depth | 持续增长 = 某阶段卡住 |
| 后台任务最大空闲 (23) | worker idle | 超时 = worker 可能死了 |
| Twitter 额度余量 (36) | credits runway | 付费额度预警 |
| NewsAPI 月度用量 · 按密钥 (39) / ScraperAPI 月度用量 (40) | api usage | 付费额度预警。NewsAPI 按密钥分开显示(`pgc_newsapi_key_tokens_pct`,1号=主):合计口径会把单把用光的密钥平均掉(7/4 主密钥烧干时合计仍 ~40% 绿) |
| NewsAPI 今日用量 · 按主题组 (64) | `pgc_newsapi_daily_tokens` / `pgc_newsapi_daily_token_cap` | 日上限执行情况(2026-07-07 起代码强制,八组上限总和=146=月配额日均线)。顶到 100% 走平=设计行为;要行动的形态只有"某组连续多天 100%"(上限长期压量,该调上限或收窄范围) |
| 发布量趋势 (24) | published/h | 吞吐节奏(与 64 并排,w12) |
| 管线日志 (25) | Loki | 排障 |

### Row 首发 — 状态与对外口径
| 面板 (id) | 指标 | 操作含义 |
|------|------|---------|
| 待处理故障 (17) | canaries_failed + critical_fire + 我方管线延迟 | 我方侧故障合计，恒 0，非 0 立即排查（红）；7/6 语义拆分后不含上游观察项 |
| 首发关注 (32) | `pgc_first_source_audit_attention` | 红了→去日报看清单加源/修源 |
| 活跃高优先延迟 (33) | active T0/T1 source latency | 现在还有哪些高优先信号慢 |
| 关键源探活失败数 (34) | canaries failed | 关键源健康 |
| 抢先率（对外口径）(63) | `pgc_first_source_win_rate` | **全板唯一对外可引用数字**，受保护条款约束（见 2026-07-06 节），删除即事故 |
| 观察清单 (61) | critical_watch + audit_attention + sla_attention 之和 | 不紧急观察项，允许有水位（黄 15 / 红 30），每周过一遍 |
| 风险趋势 (37) | owner action components | 待处理事项是好转还是恶化 |
| 活跃延迟源明细 (62) | 迟到来源明细（已去重转译）| 定位迟到来源：我方管线慢 → 查抓取解析；上游晚发 → 评估换快渠道 |

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
| 2026-06-30 | 清理：删 8 个空的双语 row(总览/Owner Cockpit 等历史遗留空壳); 修护栏区两处面板重叠(51/58、7/60 共占 x16); 各域召回→各域丢弃占比(召回排除丢弃后恒≈100%, 无信号); 两个 Deep Dive row 改名区分(诊断·对标 / 工程诊断·信号延迟) | **44** (含 4 row+text) |
| 2026-07-01 | Pascal 反馈"看不懂"驱动的黑话清理。砍：噪声泄漏(按类型)、哪些类别需要马上处理/当前哪些信源正在拖慢(与"现在先处理哪些信源"同源数据切3刀)、这些超时是事故还是回补噪音(原始kind枚举表)、源SLA关注(与🔬row重复)；整个🔬工程诊断row全砍(7面板，清一色原始Prometheus多列标签表，改名也救不了)；信源可信度构成(按标签占比)；各源延迟明细(同款原始标签表)。剩余面板去黑话：`Source SLA 是否破线`→信源更新是否及时、`哪些源违反 SLA`→哪些信源更新慢了、`Canary 失败`→关键源现在挂了吗、`Twitter runway`→Twitter 还能撑几天，相关英文 description 一并译成中文 | **30** |

| 2026-07-02 `#58/#59` | 榜单口径切换(大模型确认制 metric v2)配套文案：面板9断点标注+win_precision图例改'旧判断器准确率'；面板17讲清与榜单胜率差7倍原因(覆盖vs速度)；面板32改行动指引+审计端去噪(pgc#18) | 30 |
| 2026-07-03 `#60`+本次 | 体检发现榜单胜率(对外口径v2)全板无处展示→加面板61(诊断区空位)；row30标题'已降级'→'对外口径·诊断'；**砍面板57旧判断器准确率**(其说明自述'低了不用管'，不过'看了会做什么'测试；数据仍在Prometheus/Loki) | **30**(26内容+4结构) |
| 2026-07-05 | owner cockpit 日检发现缺少显式“当前行动总数”和“信源 SLA”stat；按净增为零原则，将面板17/61替换为这两个行动面板，趋势和明细仍保留在37/62。同步更新本地 dashboard validator，防止继续按旧 76 面板时代的 section/panel id 误报。 | **30**(26内容+4结构) |
| 2026-07-06~07 `#62~#67` | 全板评审落地 + 行动语义拆分(17/61) + NewsAPI 日耗面板(64) + 判定年龄第二值(7) + 发布量基线(24) | **33**(29内容+4结构) |
| 2026-07-08 `#69` | 合并 #67/#68 + 全板措辞统一 + 对抗评审 25 项修正（阈值按 7 天实测重校、面板 62 去重转译、单位/图例/虚线落实、validator 结构闸） | **33**(29内容+4结构) |
| 2026-07-16 | 面板9加「可赢胜率」线(pgc_event_winnable_win_rate, pgc 侧同日新增)：总胜率混入记者独家原创类结构性输局(实测约六成)，Pascal 看到 26% 被吓到但其中大半不可赢；口径=对手是转发/通稿/官方公告(a_type 非 original_reporting/commentary)。原「胜率」图例改「总胜率(%·含赢不了的)」。防解释第二遍：description 写明"总低可赢高=新闻结构非故障" | **33**(29内容+4结构) |

**教训**：76 面板之所以出现，是因为每次有新指标就加面板，没人问"看了会做什么"。
回退之所以发生，是因为另一个 session 看到"面板少了"就以为是 bug。
写这份文档就是为了断掉这个循环——改 dashboard 之前先读设计原则。

## 怎么改这个 Dashboard

1. 先读完本文档的设计原则。
2. 新面板必须回答"看了之后我会做什么动作"。
3. 内容面板以 25 为目标上限（2026-07-08 现状 29，历史欠账）：**净增为零，要加必须同 PR 先砍一个**，有机会就向 25 收敛。validator 会在超过现状时报错。
4. 改完在仓库根目录跑 `python3 scripts/local/validate_pgc_grafana_dashboard.py`（结构闸：重复 id / gridPos 重叠 / 面板预算 / 黑话 / 关键面板锚定）。上线前可选加 `--ssh-host aliapmo --prometheus-url http://localhost:9091` 对 prod 逐条验查询（两个参数必须同时给）。
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
- `pgc_first_source_win_rate_by_domain`
- ~~`pgc_first_source_win_rate`~~ — **2026-07-03 起由面板 63 抢先率（对外口径）重新消费**，受保护条款约束（见 2026-07-06 节），勿按本节处理
- `pgc_first_source_losses_by_reason`
- `pgc_first_source_no_first_party`
- `pgc_first_source_leaderboard_age_seconds`
- `pgc_first_source_lead_median_hours` / `lag_median_hours`
- `pgc_first_source_confident_races`

> **2026-06-30 校正**：旧版本曾称 `pgc-first-source-leaderboard.timer` 已 disable —— 这是**错误**的。
> prod 上该 timer 仍 `enabled`+`active`(每日 10:30 CST)，因为 `pgc_first_source_speed_win_share`
> (首发走势面板的"抢先率"线)仍由它产出的 leaderboard 报告驱动。改前以
> `ssh aliap systemctl list-timers 'pgc-*'` 实际状态为准。

## 已退役的指标（2026-07-01）

🔬工程诊断 row 整体砍除后，以下指标不再有任何面板消费（Gauge 定义仍在 `metrics.py`，避免 scrape error）：
- `pgc_broadcast_noise_by_type` — 原"噪声泄漏(按类型)"
- `pgc_signal_latency_actionable_breaches_3h` — 原"高优先级信号仍在超时吗"系列
- `pgc_signal_latency_breach_kind_24h` — 原"这些超时是事故还是回补噪音"
- `pgc_broadcast_reliability_share_pct` — 原"信源可信度构成"
- `pgc_source_health_sla_attention_source` — 原"哪些源违反 SLA"明细表

`pgc_signal_latency_active_source_breaches_3h`、`pgc_source_health_sla_attention`、`pgc_source_health_canaries_failed` 仍被保留面板（风险趋势 / 信源更新是否及时 / 关键源探活失败数）消费，未退役。`活跃高优先延迟` 和风险趋势里的同名序列统计 `source_latency` 与 `source_feed_lag`，避免上游 feed 晚到的 T0/T1 active breach 只出现在明细表、首屏 stat 却为 0。

## 2026-07-07 口径修正：低延迟 owner 视图必须看见 feed-lag

日检发现 7/6 收窄后，`活跃高优先延迟` 只看 `source_latency`，会把 `source_feed_lag`
这类同样 actionable 的 T0/T1 慢源从首屏 stat 和风险趋势中藏掉。修正：面板 33 和风险
趋势里的“高优先级延迟”序列恢复为 `source_latency|source_feed_lag`；面板 17
`需要动手吗(火情)` 仍只看我方契约侧故障，避免把上游潮汐误报为生产事故。

## 2026-07-06 口径修正：行动类面板只数"我们的错"

宽口径（kind 含 source_feed_lag）让 33/17/37 三个行动类面板跟着新闻潮汐呼吸
（午后/开盘 8→63，Pascal 两天内三次误读为事故）。修正：**stat/趋势类行动面板一律
kind="source_latency"（我们的错，平时=0，非0即查）**；上游 feed 迟到的观察留在
明细表(62, 双口径保留)与每周迟到画像（换快通道的依据，例：Bluesky-Bloomberg）。
教训回填设计原则："看了会做什么"不仅约束加面板，也约束改口径——一个让人反复问
"这数对吗"的面板等于坏面板。

## 2026-07-06 全板评审落地（Pascal: "整个面板还有啥要改进的"）

- **面板 63 榜单胜率(对外口径) 第二次恢复**——7/3 补过一次(当时叫61)，后被改成信源SLA
  又homeless。**保护条款：这是全板唯一对外可引用数字，改板前先搜 pgc_first_source_win_rate
  确认有家，删它=事故。**
- 行动语义拆分：17=需要动手吗(火情, canary+critical_fire+我方延迟, 恒0)、61=观察清单
  (critical_watch+首发关注+SLA, 允许有水位)。背景：旧"行动总数"把3个安静博客算成
  "7个要行动"吓到 Pascal。风险趋势(37)同步双线。
- 发布量趋势(24)加"上周同时刻"offset 7d 基线——"是不是发少了"直接看图。
- 判定数据年龄(7)加第二值=二判不可用数（配 pgc 侧 dual_judge_down critical 告警，
  7/5 全天0确认事故的疫苗）。
- 面板 9 删"旧判断器准确率"线（已降级诊断项混在北极星走势里）。
- 防解释第二遍描述：53(policy域基线~50%)、52(±3pp采样噪声带)、5(分母<80胜率不可信)。
- **格式规范化**：pgc-pipeline.json 已归一为 json.dumps(indent=2, ensure_ascii=False)+\n
  ——以后一律程序化编辑（json.load→改→dump），手工拼接时代结束（当晚拼坏三次）。

## 2026-07-07 口径修正：stage 双计去重

Pascal 问"活跃高优先延迟=46 对吗"——46 是 index/push 两个测量 stage 直接相加：
同一条目在两个 stage 各记一次（T1 的 index SLA 30min、push SLA 60min 是独立阈值），
实际去重后约 30。修正：17/33/37 三个 stat/趋势面板一律 `max(sum by (stage)(...))`
——不双计，且发布队列卡住（只在 push 侧超时）时仍能浮出。明细表(62)保留按 stage
的原始行。当日实测：33 面板 46→30。


## 2026-07-07 全板措辞与风格统一（Pascal: plain professional clean clear）

- 三个 row 标题去 emoji；聊天式提问标题一律改为简洁名词短语（"需要动手吗(火情)"→"待处理故障"、
  "关键源现在挂了吗"→"关键源探活失败数"、"NewsAPI 今天各组用了多少"→"NewsAPI 今日用量 · 按主题组"、
  "榜单胜率(对外口径)"→"抢先率（对外口径）"）。图例同步："火情合计(应为0)"→"待处理故障（应为 0）"、
  "高优先级延迟"→"高优先延迟"。
- 描述统一为三段式：测什么 / 什么算正常 / 什么时候要行动。删除描述里的内部变更史
  （"替换了原先误导的…""7/6又被误删…"），这类记录只留在本文档。人名不进面板。
- 面板 63 的防删说明保留一句（对外口径唯一，删除即事故），其余历史细节在本文档。
- 本节以前小节里出现的旧面板名是历史记录，以当前 JSON 为准。validator
  （scripts/local/validate_pgc_grafana_dashboard.py）的 row 名单已同步。


## 2026-07-08 对抗评审轮（合并 PR #69 前，4 维 32 agent）

Pascal 批准前的全板对抗评审（PromQL 逐条实测 / JSON 完整性 / 措辞新眼 / 文档与部署），
25 项确认全部修正。要点：

- **阈值与文字必须同一句话**：17 恒0语义→非0即红；33 是潮汐观察量，按 7 天实测
  （中位 29 / p90 171 / 峰 520）重校为黄 150 / 红 400 且只染数值不染底；61 观察清单
  实测最高 13→黄 15 / 红 30 只染数值。教训：改描述时没人对照 thresholds，从此
  "描述说正常的水位不许把面板染红"。
- **面板 62 表格转译**：原始 topk(12) 双 stage 混排加不出任何面板上的数——改为
  按条目去重（max by source,kind）+ 列名/枚举全转中文（我方管线慢 / 上游晚发），
  与 33 同口径可对账。
- **面板 64 诚实化**：cap 执行有 +1 越顶（上限越小越明显，E/F/G 实测 111–120%），
  max 100→125、描述写清越顶属正常；管线侧 off-by-one 另开 eigenflux-pgc PR 修。
- 落实虚线基线(24)、二判计数单位(7)、percent 单位(53)、看板级 description 北极星、
  37/60 图例、p90→"最慢的一成条目"、key→密钥。
- validator 新增结构闸：重复 id / gridPos 重叠 / 面板预算(>29 报错) / 黑话扫描
  (critical|SLA|canary|p9x|kind=，标题与图例)；清空 76 面板时代的空豁免 id。
- 本文档：历史小节恢复当时的旧面板名（历史记录不改写），速查表全面同步 JSON，
  已退役指标表为 pgc_first_source_win_rate 加"已被 63 重新消费"标注。
