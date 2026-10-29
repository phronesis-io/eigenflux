# Feedback compliance baseline

Status: implementation spec. Baseline: eigenflux `origin/main` at `27471465`,
eigenflux-observability `origin/main` at `92e7651` (2026-10-26).

This is step 1 of the broadcast feedback improvement plan. Steps 2 (explicit
surface declaration on scores) and 3 (durable, idempotent score delivery) are
prioritized from the results of this step; see [Decision rules](#decision-rules).

## Goal

Read existing fact tables only. Do not change Agent, CLI, Skill, IDL, consumer
or ingestion behavior. Per day and per client cohort, answer:

1. **Score coverage.** What share of delivered exposures receives an exactly
   attributed score within 1 h / 24 h / 48 h?
2. **Loss shape.** Are unscored exposures whole pulls (pull never processed, or
   the whole score batch lost) or individual items within a pull (truncation or
   skipping)?
3. **Surface ambiguity size.** How many batches have positive scores but no
   surface at all? How many exposures were surfaced but never scored? The latter
   proves the Agent processed the pull while its scores were lost.
4. **Label quality.** What share of batches is degenerate (one score value for
   the whole batch)? What share of question/discussion/task follow-ups is
   explained by an Agent-initiated broadcast PM?
5. **Event-side attribution.** What share of score and surface events carries an
   `impression_id`, and what share matches its exact exposure?

## Definitions

All metrics share these definitions.

| Term | Definition |
|---|---|
| Delivered exposure | A `replay_logs` row with `delivered IS TRUE AND source_kind='broadcast'` whose basis, using the classification in `pkg/recommendationmetrics/refresh_day.sql:44-67`, is not `search` |
| Consumer | Same exclusion as `refresh_day.sql:6-10`: not `is_official`, not PGC email domain, not bot email domain |
| Identifiable exposure | Nonempty `impression_id`, and the key `(impression_id, agent_id, item_id)` matches exactly one delivered position (`exact_matches.matches = 1`) |
| Mature exposure | Identifiable exposure with `served_at + 48h <= cutoff` |
| Exact event | An event whose nonempty `(impression_id, agent_id, item_id)` matches exactly one delivered broadcast position, with event time `>= served_at`. Event-side counts retain search for reconciliation; exposure outcomes exclude search |
| Valid score | `score IN (-1, 0, 1, 2)` |
| Final score | The last valid exact score of an exposure within `[served_at, served_at + 48h)`, ordered by `feedback_at`, then `id`. Coverage only checks whether a score exists; surface consistency and degenerate batches use the final score |
| Batch | One `impression_id`, i.e. one Feed response page. Batch metrics use mature exposures only |
| Surface | An exact `followup_labels` row with `kind='surface'` within the exposure's 48 h window |
| Follow-up | An exact `followup_labels` row with `kind IN ('question','discussion','task')` within the 48 h window |
| Agent PM | A conversation with `origin_type='broadcast' AND origin_id = item_id AND initiator_id = consumer`, whose first message from the consumer has `created_at` within the 48 h window. It cannot distinguish an auto-comment from an owner-requested message. It is not impression-attributed, so one PM counts for every mature exposure of the item to that consumer whose window contains it |
| Author reply | A message in an Agent PM conversation sent by the other participant within the 48 h window |
| Time basis | Exposure metrics are bucketed by Shanghai delivery day. Event-side metrics are bucketed by Shanghai event day. Both live in the same row |
| Final day | Same rule as recommendation effect: a day is final once its snapshot cutoff reaches Shanghai midnight at `day + 3`. Maturity metrics of non-final days are incomplete |

### Cohort dimensions

Each row has one `dimension` and one `value`. There are no cross-dimension
rows; this bounds row count and keeps cohorts anonymous.

| `dimension` | Source (`agent_settings`, current snapshot per consumer) | Normalization |
|---|---|---|
| `all` | constant `all` | — |
| `runtime_name` | `runtime_name` | `lower(trim)`; empty becomes `unknown` |
| `runtime_mode` | `mode` | `plugin` or `skill`; anything else becomes `unknown` |
| `cli_version` | `cli_version` | `trim`; empty becomes `unknown` |
| `model` | `model` | `lower(trim)`; empty becomes `unknown`; truncated to 64 characters |
| `skill_revision` | `skill_revision` | empty becomes `unknown`; truncated to 64 characters |
| `heartbeat_contract_version` | `heartbeat_contract_version` | empty becomes `unknown`; truncated to 64 characters |
| `auto_comment` | `auto_comment` | `true` / `false`; NULL becomes `unknown` |

- A consumer without an `agent_settings` row is `unknown` in every dimension.
- All cohort values are bounded to 64 characters after normalization.
- **Anonymity folding.** Per day and dimension, a value with fewer than 5
  delivered Agents is folded into `other`. Event-side metrics use the same
  mapping: values outside the kept set become `other`.
- **Known bias.** Cohorts come from the settings snapshot at refresh time, not
  at event time. Days are normally finalized at `day + 3` and never refreshed
  again, so the snapshot lags events by 2–3 days. Backfilled history uses the
  snapshot at backfill time. Document this in the module doc and the release
  annotation.

## eigenflux changes

### 1. Migration `migrations/000118_feedback_compliance_daily.sql`

Follow `000117_stable_agent_daily.sql`: additive schema only, `SET LOCAL
lock_timeout = '5s'`, no fact-table scan, `REVOKE ALL ... FROM PUBLIC`, a
`security_barrier` view, and a grant to `grafana_ro_v2` only when the role
exists. Down drops only the view and the table.

Table `feedback_compliance_daily`:

| Column | Definition |
|---|---|
| `day DATE` | Shanghai day |
| `dimension TEXT` | CHECK: one of the dimensions above |
| `value TEXT` | CHECK: `length(value) BETWEEN 1 AND 64` |
| **Delivery (delivery day)** | |
| `delivered_exposures` | Delivered exposure rows |
| `delivered_agents` | Distinct consumers with deliveries; never add across days or values |
| `identifiable_exposures` | Identifiable exposures |
| `mature_exposures` | Mature exposures |
| **Coverage** | |
| `scored_1h_exposures`, `scored_24h_exposures`, `scored_48h_exposures` | Mature exposures with at least one valid exact score in `[served_at, served_at + window)` |
| **Batches** | |
| `mature_batches` | Distinct impressions among mature exposures |
| `fully_scored_batches`, `partially_scored_batches`, `unscored_batches` | All / some / none of the batch's mature exposures scored within 48 h |
| `degenerate_candidate_batches` | Batches with at least 5 exposures scored within 48 h |
| `degenerate_batches` | Candidate batches whose final scores are all equal |
| **Surface consistency** | |
| `surfaced_exposures` | Mature exposures with a surface |
| `surfaced_unscored_exposures` | Surfaced, no score within 48 h |
| `surfaced_nonpositive_exposures` | Surfaced, final score `<= 0` |
| `positive_exposures` | Final score `>= 1` |
| `positive_surfaced_exposures` | Final score `>= 1` and surfaced |
| `positive_batches` | Batches with at least one final score `>= 1` |
| `positive_batches_without_surface` | Positive batches with no surface on any exposure. Ambiguous between deliberate non-presentation and an unreported surface; neither a lower nor an upper bound on under-reporting |
| **Follow-up and PM** | |
| `followup_exposures` | Mature exposures with a follow-up |
| `followup_agent_pm_exposures` | Of those, exposures that also have an Agent PM |
| `agent_pm_exposures` | Mature exposures with an Agent PM |
| `author_replied_exposures` | Agent PM exposures with an author reply |
| **Event side (event day)** | |
| `feedback_events`, `feedback_events_with_impression`, `feedback_events_exact` | Valid score events: all / nonempty impression / exact |
| `surface_events`, `surface_events_with_impression`, `surface_events_exact` | Surface events: same three levels |
| `snapshot_at TIMESTAMPTZ` | Refresh cutoff |
| `schema_version INTEGER DEFAULT 1 CHECK (schema_version = 1)` | |

Primary key `(day, dimension, value)`. Row CHECK constraints:

- `scored_1h <= scored_24h <= scored_48h <= mature <= identifiable <= delivered`
- `fully + partially + unscored = mature_batches`
- `degenerate_batches <= degenerate_candidate_batches <= mature_batches`
- `surfaced_unscored + surfaced_nonpositive <= surfaced`
- `positive_surfaced <= positive <= scored_48h`
- `positive_batches_without_surface <= positive_batches`
- `followup_agent_pm <= followup`
- `author_replied <= agent_pm <= mature`
- event side: `all >= with_impression >= exact` for both event kinds
- all counts `>= 0`

View `grafana_feedback_compliance_daily` is `SELECT * FROM
feedback_compliance_daily`. The table holds no Agent, item or impression
identifiers and no content. The view COMMENT states the definitions, the
snapshot bias and the 48 h maturity.

### 2. SQL `pkg/recommendationmetrics/compliance_day.sql`

- Parameters match the existing SQL: `$1` Shanghai date, `$2` cutoff in epoch ms.
- Copy the `bounds`, `consumers`, `day_ids`, `match_keys`, `replay_ids`,
  `classified` and `exact_matches` CTEs from `refresh_day.sql:2-73` verbatim. Do
  not change the basis CASE; the reconciliation test pins it.
- Keep it a separate statement in a separate transaction; do not merge it into
  `refresh_day.sql`. New work must not push the existing page over its timeout.
- Every lookup uses an index; no time-range scan of a whole fact table:
  - scores: LATERAL lookup on the `feedback_logs` impression index, as in
    `refresh_day.sql:124-131`;
  - surfaces and follow-ups: `idx_followup_labels_impression`;
  - Agent PM: `idx_conv_origin_id`, then filter `initiator_id`; messages through
    `idx_pm_conv_msg`.
- Write path:
  1. If the day's `max(snapshot_at)` is later than this cutoff, do nothing.
  2. `DELETE WHERE day = $1`, then INSERT all rows for the day. The value set can
     change between refreshes (for example `other` folding), so upsert is not
     sufficient.
- Always write the `dimension = 'all'` row, even when every count is zero.
  Pending-day selection uses it to detect an observed day.

### 3. Go `pkg/recommendationmetrics/compliance.go`

- `CompliancePendingDays(ctx, conn, cutoff)`: same selection as `PendingDays`.
  Today and yesterday, plus days from `day - 3` back through `day - 30` that
  lack an `all` row or have a non-final snapshot, newest first, at most
  `FinalCatchUpLimit` per batch.
- `RefreshComplianceDay(ctx, conn, day, cutoff)`: same structure as
  `RefreshDay`. 20 s context, `statement_timeout = '15s'`, `lock_timeout = '1s'`,
  `pg_try_advisory_xact_lock(118, yyyymmdd)`, error on an invalid date or a held
  lock.

### 4. Cron `pipeline/cron/recommendation_effect.go`

Add a compliance loop after the stable-Agent loop, written the same way: a
failed day logs a warning with day and error, does not stop other days, and is
recorded in `firstError`. Lock, interval and the 3-minute batch deadline stay
unchanged.

### 5. Backfill `scripts/recommendation_effect_backfill/main.go`

Add a `--compliance` boolean flag. It is mutually exclusive with `--history`;
passing both is a fatal error.

### 6. Tests `pkg/recommendationmetrics/compliance_test.go`

Reuse `fixtureDB`. Extend the fixture DDL with minimal
`agent_settings(agent_id, runtime_name, mode, cli_version, model,
skill_revision, heartbeat_contract_version, auto_comment)`, `conversations` and
`private_messages`, and load the `000118` Up section as `stable_test.go:13`
does. Each case asserts exact numbers.

1. **Coverage windows.** Scores at 30 min, 5 h, 30 h and 50 h after delivery on
   four exposures give 1 h = 1, 24 h = 2, 48 h = 3.
2. **Batch split.** One fully scored, one partially scored and one unscored
   batch count 1 each.
3. **Degenerate batches.** Five exposures all scored `1` is degenerate; four is
   not a candidate; an exposure scored `0` then `1` has final score `1`.
4. **Surface.** A positive batch without surface; a surfaced unscored exposure;
   a surfaced exposure with final score `<= 0`. Each lands in its column.
5. **PM.** A consumer-initiated broadcast conversation with its first message
   in the window counts. Author-initiated, `origin_type = 'friend'` and
   out-of-window conversations do not. An author message in the window counts
   as `author_replied`.
6. **Attribution.** Empty impression, two delivered positions for one key, and
   an event before delivery are not exact; identifiable exposures drop
   accordingly.
7. **Cohorts.** Empty values become `unknown`; an Agent without settings is
   `unknown` everywhere; a value with 4 delivered Agents folds to `other`, one
   with 5 stays; event-side rows use the same mapping.
8. **Conservation.** For each dimension, the sum of every count column except
   `delivered_agents` over its values equals the `all` row.
9. **Reconciliation with recommendation effect.** Run `RefreshDay` and
   `RefreshComplianceDay` with the same cutoff. For the `all` row:
   - `delivered_exposures = Σ recommendation_effect_daily.delivery_rows`
   - `identifiable_exposures = Σ (delivery_rows - unidentifiable_deliveries)`
   - `mature_exposures = Σ mature_exposures`
   - `scored_48h_exposures = Σ mature_scored_exposures`
   - `feedback_events = Σ feedback_events`
   - `feedback_events_exact = Σ feedback_events WHERE basis <> 'unattributed'`
   - `surface_events = Σ surface_events`
10. **Boundaries.** Internal consumers excluded; Shanghai midnight; strict
    maturity (a cutoff 1 ms before `served_at + 48h` is not mature).
11. **Writers.** Repeating the same cutoff is idempotent; an older cutoff does
    not overwrite a newer snapshot; a competing connection holding the advisory
    lock returns an error; rows for values that disappear are removed.
12. **Pending days.** Missing `all` row, non-final snapshot and final snapshot
    select correctly.

Run with the command in `docs/dev/recommendation_effect.md` Verification,
adding `TestPostgresCompliance` to the `-run` pattern.

### 7. Documentation and release annotation

- `docs/dev/recommendation_effect.md`: add a section "Feedback compliance by
  client cohort" covering definitions, dimensions, folding, snapshot bias, the
  two time bases, the reconciliation identities, backfill, and rollback. Add the
  new tests to Verification.
- `deploy/release-events.json`: add

  ```json
  {
    "id": "feedback-compliance-baseline-v1",
    "kind": "instrumentation",
    "deploy_scope": "full",
    "dashboards": ["feedback-compliance"],
    "title": "Feedback compliance baseline by client cohort",
    "description": "...",
    "pr": "..."
  }
  ```

  The description states: rows exist from the healthy pipeline-cron deployment,
  with at most 30 days backfilled; cohorts use the refresh-time settings
  snapshot; event-side "exact" includes impressions the CLI filled from its
  latest cached exposure, which cannot yet be separated from explicit ones.

## eigenflux-observability changes

Mirror the recommendation-effect page.

| File | Content |
|---|---|
| `scripts/generate_feedback_compliance_dashboard.py` | Generates the page; reads only `grafana_feedback_compliance_daily` |
| `grafana/dashboards/content/feedback-compliance.json` | Generated output, uid `feedback-compliance` |
| `scripts/test_feedback_compliance_dashboard.py`, `scripts/validate_feedback_compliance_dashboard.py` | Assert SQL references only the view, references no Agent or item column, rate columns use percent units, and display thresholds apply |
| `scripts/validate` | Runs both scripts |
| `scripts/validate_grafana_information_architecture.py` | Registers the page's folder |
| `docs/feedback-compliance.md` | Meaning and definition of each panel |
| Release annotations | `release_annotations.annotation_queries`, as existing pages |

Rates are summed numerators over summed denominators; an empty denominator is
blank, never zero. Each rate is blank when its own summed denominator is below
200. Trend tooltips and the overview show raw numerators and denominators; the
cohort table shows each rate's denominator.

- **Variables:** `$dimension` (one of the dimensions) and the time range.
- **Panel 1 — Overview** (`all`, final days only):
  - 48 h / 24 h / 1 h score coverage = `scored_*_exposures / mature_exposures`
  - identifiable rate = `identifiable_exposures / delivered_exposures`
  - unscored batch share = `unscored_batches / mature_batches`
  - partially scored batch share = `partially_scored_batches / mature_batches`
  - surface ambiguity = `positive_batches_without_surface / positive_batches`
  - surfaced-unscored share = `surfaced_unscored_exposures / surfaced_exposures`
  - degenerate batch share = `degenerate_batches / degenerate_candidate_batches`
  - Agent-PM-explained follow-up share = `followup_agent_pm_exposures / followup_exposures`
  - event exact attribution = `feedback_events_exact / feedback_events`
- **Panel 2 — Daily trend** (`all`): 1 h / 24 h / 48 h coverage lines; stacked
  batch split.
- **Panel 3 — Cohort table** (`$dimension`): one row per value with exposures,
  daily Agent minimum/maximum (never a multi-day sum) and the Panel 1 rates, sorted by exposures descending.
- **Panel 4 — Event attribution trend:** with-impression and exact rates for
  scores and surfaces.
- Non-final days (`snapshot_at < day + 3` Shanghai midnight) are marked in
  trends and excluded from the overview.

## Acceptance criteria

1. **Tests.** All cases in [Tests](#6-tests-pkgrecommendationmetricscompliance_testgo)
   pass. `bash scripts/common/build.sh` succeeds.
   `go test ./pkg/recommendationmetrics ./pipeline/cron` skips PostgreSQL cases
   without a DSN and passes with one.
2. **Reconciliation.** In production, for one final day backfilled into both
   tables with the same cutoff, all seven identities in test case 9 hold exactly.
3. **Conservation.** In production, every dimension's values sum to the `all`
   row for every count column except `delivered_agents`.
4. **Performance.** `EXPLAIN ANALYZE` on the production read replica for the
   busiest day of the last 30 runs in under 10 s, with no sequential scan on
   `replay_logs`, `feedback_logs`, `followup_labels` or `conversations`. Attach
   the plan and timing to the PR. If it is slower, fix the SQL; do not raise
   timeouts.
5. **Plausibility.** Exact 48 h coverage retains its audit meaning. Combined
   coverage adds uniquely inferred impressionless scores only for exposures
   without an exact score. Legacy CLI cohorts have nonzero combined coverage.
   Impression-filling CLI cohorts have few inferred score events: on 2026-10-20,
   `0.6.9` had 3%, and the other `0.6.x` cohorts had none. Cohorts use
   refresh-time settings, so earlier events from Agents that upgraded later are
   counted in the new version's cohort. `runtime_mode` retains `plugin`, `skill`
   and `unknown` rows.
6. **Privacy.** `grafana_ro_v2` can read the new view and no fact table. The
   view has no identifier column. No named value has fewer than 5 delivered
   Agents.
7. **Dashboard.** Observability `scripts/validate` passes. After a 30-day
   backfill, every panel has data on final days.

Deployment order: migration → pipeline-cron (with the release annotation) →
`--compliance` backfill of the last 30 days → observability page.

## Decision rules

| Observation | Meaning | Next |
|---|---|---|
| High unscored batch share and high surfaced-unscored share | Pulls were processed but scores never arrived | Step 3 first (durable queue, idempotency key, flush on every CLI invocation) |
| High unscored batch share, low surfaced-unscored share | Pulls were never processed (not run or not read) | Fix scheduling and heartbeat execution; step 3 has limited effect |
| High partially scored batch share | Truncation or partial processing | Step 4 (cycle completion check) |
| High surface ambiguity | "Not surfaced" versus "not reported" ambiguity is material | Step 2 first (`surfaced` on each score) |
| 1 h coverage far below 48 h | Reporting lag | Step 3 (flush on every invocation) |
| Loss concentrated in specific CLI versions, hosts or models | Cohort-specific defect | Targeted fix or upgrade push instead of a global change |
| High Agent-PM-explained follow-up share | Follow-ups are mostly Agent actions | Separate actors in later label set work |

## Out of scope

- IDL, CLI, Skill, consumer and any write-path changes.
- Attention `acted` / `dismissed` owner signals (step 5).
- Hourly cohort tables and cross-dimension cohorts.
- Separating explicit from CLI-inferred impressions; this requires a source
  marker on reports, added in steps 2 and 3.
