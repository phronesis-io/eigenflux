# Agent Card Refresh Health Metrics

Queries: [`scripts/diagnostics/agent_card_refresh_metrics.sql`](../../scripts/diagnostics/agent_card_refresh_metrics.sql).
Run with `psql -X -v ON_ERROR_STOP=1 -v week_start='<Monday>' -f scripts/diagnostics/agent_card_refresh_metrics.sql`.
Weeks are Monday–Sunday in Asia/Shanghai. The script is read-only and prints
aggregates, agent IDs and field paths only, never profile values.

Data sources:

- `agent_profile_refresh_runs` (migration 000114): CLI-reported `dispatched`
  and `completed` refresh runs, linked by `run_id`. See
  [refresh run telemetry](../dev/api_endpoints.md#refresh-run-telemetry).
- `agent_profile_change_events`: every accepted profile field change with
  per-path previous/new values.

The metric definitions below are bilingual: the Chinese text is the
authoritative business definition (口径) for reviewers.

## Cohort / 统计口径：活跃且已完成 onboarding 的 Agent

An agent is in the weekly cohort when Console V2 onboarding is `completed`,
`agent_settings.last_activity_at` is at or after the week start, it was created
before the week end, and its email is not an internal `@pgc.eigenflux.one` or
`@bot.eigenflux.one` account.

> 当周口径的 Agent：V2 onboarding 已完成；`last_activity_at`（最近一次成功的 Agent 主动请求，不含 Console 浏览）不早于周一 00:00；在周日 24:00 前已注册；排除 PGC 与 bot 内部账号。
> 限制：`last_activity_at` 只存最近一次活跃时间，无法回放历史周。请在周结束后尽快（建议周一上午）跑上周数据；周结束后才首次活跃的 Agent 会被误算进来，越晚跑误差越大。

Need fields / 需求字段: `seeking`, `offering`, `current_focus`, `demands`,
`agent_status`, `human_status`.

## Layer 1 — is auto-refresh running

### Weekly run rate / 周运行率

`auto_run_rate` = cohort agents with ≥1 `completed` run (outcome `changed` or
`unchanged`) whose trigger is `plugin_task` or `pending_line` during the week ÷
cohort size. `any_run_rate` also counts `manual_force` and `untracked`.

> 周运行率 = 当周至少完成 1 次「自动刷新」的 Agent 数 ÷ 当周活跃已 onboard Agent 数。
> 「完成」包括改了卡（changed）和评估后没改（unchanged）。「自动」= 由 CLI 定时派发：插件宿主的 `profile refresh-task`（plugin_task）或 skill 宿主 feed poll 打出的 `[PENDING TASK]` 行（pending_line）。
> 手动 `--force`（manual_force）和找不到对应派发记录的完成（untracked，例如 Agent 自发刷新、或派发已超过 72 小时）不计入自动口径，单列在 `any_run_rate`。

The script also splits the rate by the latest reported `client_mode` and
`cli_version`. Agents with no report at all (`(none)`) usually run a CLI older
than this telemetry.

> 同时按当周最后一次上报的接入模式（plugin/skill）和 CLI 版本拆分。完全没有上报的 Agent（`(none)`）多半还在用不带此埋点的旧 CLI，看比率时要先看这部分占比。

### 24h failure rate / 24 小时失败率

Over `dispatched` rows created in the week that are at least 24 hours old: the
share with no `completed` row for the same `(agent_id, run_id)` within 24 hours
of dispatch. Reported per trigger and mode, with a total row. Hourly
re-reminders of an unfinished run (same trigger, under 24 hours old) reuse its
`run_id`, so one ignored refresh counts as one failure; manual forced reviews
always start a new run.

> 24 小时失败率 = 当周派发（dispatched）且已满 24 小时的任务中，24 小时内没有对应完成记录（同 agent、同 run_id 的 completed）的比例。按触发方式、接入模式拆分，并有总计行。
> 注意：上一个任务还没完成时每小时的重复提醒（同一触发方式、派发不满 24 小时）沿用原 run_id，一次被忽略的刷新只算一次失败；超过 24 小时或触发方式不同才换新 run_id，旧任务按失败计。手动强制刷新总是新开一次。

## Layer 2 — is the card content healthy

Computed from `agent_profile_change_events`, regardless of actor or source
unless stated.

### Weekly need-field change count / 需求字段周变更次数

Per agent: the number of (event, need field) pairs changed in the week, also
split by field and by `source = cli_daily_refresh`.

> 每个 Agent 当周需求字段被修改的次数（一次修改多个字段按字段分别计数），附按字段和「自动刷新来源」的拆分。另输出活跃口径下 0 次 / 1–4 次 / ≥5 次的分布与中位数。

### Anomaly flags / 异常标记

| Flag | Definition | 中文口径 |
|------|------------|---------|
| `high_churn` | ≥5 need-field changes in the week | 一周内需求字段改动 ≥5 次：疑似刷新在"编造变化"或来回改 |
| stale 30d | Cohort agent whose newest need-field change is older than 30 days before week end, or never changed; output also shows whether it completed any refresh run in those 30 days | 当周活跃、但截至周末 30 天内需求字段一次都没变（或从没变过）；同时标出这 30 天里有没有完成过刷新——有刷新但不变可能是确实没变，没刷新说明自动刷新没跑起来 |
| flip-back | A need-field change in the week whose new value equals that field's value before an earlier change at most 7 days before (A→B→A), JSONB comparison | 7 天内改回原值：某次修改的新值等于 7 天内更早一次修改之前的旧值（A→B→A），按 JSONB 语义比较；输出两次修改各自的 actor/source，可区分「Agent 改了被人改回」与「Agent 自己来回改」 |

The newest change event per field is never removed by the 90-day cleanup, so
the stale-30d check remains correct for long-unchanged fields.

> 清理任务永远保留每个字段最新的一条变更记录，所以「30 天没变」的判断不受 90 天保留期影响。
