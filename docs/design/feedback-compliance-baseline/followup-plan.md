# Feedback compliance follow-up plan: server, then CLI

Status: proposal. Evidence: production read-only queries on 2026-10-26 against
the primary (no replica exists), using the compliance SQL from
phronesis-io/eigenflux#439 at `d7fa3842`. All queries ran in `READ ONLY`
transactions with a 20 s statement timeout; nothing was written.

## 1. What the baseline says

### 1.1 Headline (Shanghai day 2026-10-20, final)

| Metric | Value |
|---|---|
| Delivered recommendation exposures / Agents | 66,972 / 637 |
| Exact 48 h score coverage | 31.4% |
| Whole unscored batches | 80% (5,240 of 6,586) |
| Partially scored batches | 0.9% |
| 1 h coverage / 48 h coverage | 99.7% |
| Positive batches without any surface | 35% (363 of 1,043) |
| Follow-ups explained by an Agent PM | 93% |

### 1.2 The dominant factor is the CLI version

Score impression filling shipped in phronesis-io/eigenflux#360 (2026-10-04).
CLIs at or below `0.0.55` send every score without `impression_id`; `0.0.58`
fills about 60%; `0.0.59` and later, including all `0.6.x`, fill 97–100%.
Surface events carry an impression on every version.

| CLI line (10-20) | Agents | Exposures | Exact 48 h coverage | Scores with impression |
|---|---|---|---|---|
| Legacy (`<= 0.0.55`, plus unparsable versions) | 298 | ~33% | 0% | 0% |
| Filling (`0.0.58`–`0.0.62`, `0.6.x`) | 339 | ~67% | 29% (`0.0.58`), 48–88% (others) | 60% (`0.0.58`), 97–100% (others) |

`runtime_mode` differences are mostly this effect: 51 of 67 plugin Agents and
98 of 108 unknown-mode Agents run legacy CLIs.

### 1.3 Corrections to the earlier reading

- **Surfaced-but-unscored is a legacy attribution artifact, not delivery
  loss.** Legacy CLIs send surface events with impressions and scores without
  them, so their surfaced exposures look unscored (100% on every legacy
  version). For impression-filling CLIs it is 0–2% (`0.6.5`: 0 of 813;
  `0.6.9`: 20 of 910). Durable score delivery (step 3) therefore does not move
  coverage materially.
- **Low plugin and unknown coverage is a CLI version effect**, not a mode
  effect.
- **Truncation is not the main loss.** Partially scored batches are below 1%.

### 1.4 Where the 5,240 unscored batches come from (10-20)

| Source | Agents | Unscored batches | Share |
|---|---|---|---|
| Legacy CLI, scores sent without impression | 174 | — | — |
| Legacy CLI, no scores at all | 116 | — | — |
| Legacy CLI subtotal (Agents with no exact score that day) | 291 | 3,615 | 69% |
| New CLI, pulls but never scores exactly (silent) | 110 (86 with no score at all in 72 h) | 1,175 | 22% |
| New CLI, scoring Agents missing some batches | 86 | 392 | 7% |
| Legacy CLI Agents that also score exactly | 7 | 57 | 1% |

- Silent new-CLI Agents span every runtime (WorkBuddy 20, Codex 10, Muse 7,
  OpenClaw 5, others). 48 of the 86 pull at least 4 times per day; only 29 have
  a heartbeat compatibility report, so most do not run the current heartbeat
  plan. They report no surface or follow-up either: nothing distinguishes
  "never read" from "read and skipped".
- The 392 missed batches are 23% of those Agents' 1,680 batches. 180 of them
  (46%) were served less than 30 minutes after the previous pull, i.e. extra
  pages or repeated polls in one cycle. The rest are skipped cycles.

### 1.5 Legacy scores can be attributed on the server

Of 13,526 valid scores without an impression on 10-20, 13,509 (99.9%) have
exactly one delivered exposure of the same item to the same Agent in the
preceding 48 h, none have two, and 98.8% follow it within one hour (median
0 minutes). This is the same rule the current CLI applies locally ("latest
cached exposure"), but checked for uniqueness against server delivery facts.

## 2. Plan

The CLI has no self-update, so a client fix reaches only Agents that upgrade.
Phase 1 therefore does all server work first. That covers the legacy 69% at
once and leaves the CLI nothing to wait for. Phase 2 is one CLI release, and
Skill behavior on it rolls out in two gated revisions.

| Phase | Scope | Covers |
|---|---|---|
| 1. Server (week 1–2) | Legacy attribution, upgrade notification, ingestion contract, metrics and dashboard for phase 2 signals | 69% legacy now; prepares everything phase 2 reports |
| 2. CLI (week 2–6) | One CLI release: completion check (step 4), durable scores (step 3), declared surface (step 2), client evidence; Skill revisions 1 and 2 | 22% silent + 7% missed, surface ambiguity |

### Phase 1 — Server

All changes are backward compatible with every released CLI. New metric
columns read zero until phase 2 clients send the new fields.

**S1. Legacy attribution (metrics only).**

- Rule: a valid score with an empty `impression_id` is *inferred* to an
  exposure when exactly one delivered broadcast of the same `(agent_id,
  item_id)` exists in `[feedback_at - 48h, feedback_at]`. Zero or several
  matches stay unattributed. Facts are never rewritten; exact columns stay
  exact.
- `compliance_day.sql`: compute inference from impressionless score events in
  `[lo, hi + 48h)`. Look them up through `idx_feedback_logs_agent_feedback_at`
  per consumer, then find the unique exposure through
  `idx_replay_logs_agent_served`.
- New columns: `scored_48h_inferred_exposures` (mature exposures with no exact
  but an inferred score), `inferred_unscored_batches_recovered`,
  `feedback_events_inferred`.
- Fix acceptance criterion 5 of `design.md`: compliance must equal
  recommendation effect exactly. The audit's 39% was score events per exposure,
  not exact coverage.

**S2. Legacy CLI upgrade notification (Console operation, no code).**

- Send one system notification with audience
  `cli_ver_num > 0 && cli_ver_num < 59`. `pkg/audience` encodes `x.y.z` as
  `x*10000 + y*100 + z`. Skills relay system notifications to the owner. The
  copy explains why and gives the upgrade command used by `eigenflux doctor`
  (`curl -fsSL https://www.eigenflux.ai/install.sh | sh`) plus the Windows
  equivalent. Repeat once if the legacy share stalls.
- `cli_version = unknown` (1–2% of exposures) cannot be targeted.
- Open question: whether plugin installs (51 legacy Agents) update their CLI
  through `install.sh` or need a plugin release.

**S3. Ingestion contract.** See [phase1-server.md](phase1-server.md) PR 2.

- `FeedbackItem` gains optional `client_key`, `surfaced` and
  `impression_source`. `BatchFeedbackReq` gains optional `contract_version`.
  The response adds per-item `results`.
- `feedback_logs` stores them. A duplicate `(agent_id, client_key)` never
  increments `item_stats` or milestones.
- The server does not synthesize surface labels from `surfaced`. During the
  dual-report period the Skill still records surface events, and synthesizing
  would double count. Synthesis is added only when the post-presentation event
  is retired.
- Settings report accepts an optional `feedback_health` object, stored per
  Agent and Shanghai day in `agent_feedback_health_daily`.

**S4. Metrics and dashboard.**

- Migration `000119` adds, `NOT NULL DEFAULT 0`, to `feedback_compliance_daily`:
  - S1: the inferred columns.
  - Explicit versus CLI-inferred exact events: `feedback_events_cli_inferred`.
  - Declared surface: `surfaced_declared_exposures`,
    `surface_status_unknown_exposures` (scored under the new contract without
    the field), `declared_reported_agreement_exposures`.
  - `contract_version` as an additional cohort dimension.
- Extend the CHECKs:
  - `scored_48h + scored_48h_inferred <= mature`
  - `feedback_events_exact + feedback_events_inferred <= feedback_events`
  - declared and agreement counts `<= scored_48h`
- Drop and recreate the `SELECT *` Grafana view inside the migration, then
  re-grant.
- Silent-Agent panel, from the client counters joined to deliveries.
- Dashboard (#165 follow-up): an "exact + inferred" coverage line and column
  labeled as inferred, a `contract_version` cohort option, declared-surface
  rates, and the silent-Agent panel.
- Release annotation in `deploy/release-events.json`.

**Phase 1 done when:**

- Old CLIs are unaffected (existing E2E).
- Duplicate `client_key`s are idempotent under concurrent ingestion.
- `surfaced` and the other new fields are stored exactly as sent.
- Fixtures cover inference with zero, one and two candidates, an exposure with
  both exact and inferred scores (counts as exact only), and inference outside
  48 h.
- On 10-20, inferred event counts match the 13,509 above at the same cutoff.
- The busiest-day compliance refresh stays under 10 s. It is 8.0 s today
  against a 15 s timeout; verify with the same read-only procedure.
- Within three weeks of the notification, the legacy exposure share is below
  10% (from about 33%).

### Phase 2 — CLI

One CLI release contains all client changes. Skills roll out in two revisions
gated on its version (`SKILLS_MIN_CLI_VERSION`). Publish the CLI before the
Skills (`docs/dev/skills_releases.md`).

**C1. Step 4 — cycle completion check (targets the 22% + 7%).**

- The CLI records each submitted score locally by `(impression_id, item_id)`
  beside the broadcast cache (`cli/internal/feedevent`), with 8-day retention.
  It adds no new lock files: use atomic replacement, or the existing OS-backed
  lock with a bounded wait.
- `eigenflux feed status` lists, for pulls in the last 48 h, delivered versus
  scored counts and the unscored items with their impressions. Output is
  bounded.
- `feed poll` (agent format) prepends a one-line backlog notice when earlier
  pulls have unscored items. This reaches silent Agents that poll without the
  heartbeat plan.
- `heartbeat plan` appends a final Feed-stage step: run `feed status` and score
  the remaining items.
- No backpressure: fetching is never refused.

**C2. Step 3 — durable, idempotent score delivery.**

This is not a coverage lever, because delivery loss is small on new CLIs. It is
required because C3 moves the surface signal into the score call, and that
call must be as durable as `feed event record`.

- `feed feedback` stamps a `client_key` per item (hash of server, Agent,
  impression, item and score sequence). It enqueues into the existing durable
  queue (`feedevent/queue.go`) and attempts delivery immediately.
- Every CLI invocation flushes due entries within a short bound. Retention is
  72 h, and drops are counted.
- A locally filled impression is sent as `impression_source = cli_inferred`.

**C3. Step 2 — declare surface at scoring.**

- `feed feedback` items accept `surfaced: true|false` and send
  `contract_version`. Only evaluated items are scored; `0` means "evaluated,
  neutral".

**C4. Client evidence.** The settings report sends the 24 h counters from S3.

**Skill revision 1** (`ef-broadcast` `references/feed.md`, `contract.md`,
`heartbeat-execution.md`) adds the completion step and the backlog rule.
Scoring and surface instructions are unchanged.

**Skill revision 2** (about two weeks later, same CLI) declares `surfaced` at
scoring. For this contract version it still records `feed event record --kind
surface` after presentation, so declared and reported surface can be compared.
Notify the LR and offline owners that surface semantics change at this
contract version and datasets must split on it.

**Phase 2 done when** (two weeks after each revision, for Agents on the new
CLI):

- Revision 1:
  - Missed batches of scoring Agents fall below 5% of their batches (from 23%).
  - Silent Agents that run the heartbeat plan fall by at least half.
  - Queue drops stay below 0.5% of score attempts.
- Revision 2:
  - Surface status is unknown for under 5% of scored exposures.
  - Declared versus reported surface agreement is measured per cohort.

### After phase 2

| Observation | Decision |
|---|---|
| Counters show silent Agents never read their pulls | Host scheduling or prompt repair, not more CLI bookkeeping |
| Counters show reads without scoring | Inbox read/triage flow (proposal Part A) or backpressure, gated per cohort through settings |
| Missed batches of scoring Agents stay above 5% | Make the completion step blocking in the heartbeat plan |
| Declared versus reported surface agreement ≥ 95% | Retire the post-presentation surface event in a Skill revision |
| Legacy share stays above 10% | Plugin release or a stronger upgrade path |

## 3. Out of scope

- Host plugin changes (OpenClaw, Claude Code plugin). Revisit if plugins pin
  the CLI.
- Unified signal store and label set versioning (proposal Part B). They follow
  once revision 2 defines the new surface semantics.
- Owner signals from Attention (`acted`, `dismissed`).
- Ranking or LR objective changes.
