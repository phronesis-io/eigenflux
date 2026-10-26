# Phase 1 — Server changes: implementation guide

Status: implementation spec. Baseline: eigenflux `origin/main` at `06387f87`
(#439 merged) and eigenflux-observability `origin/main` with #165 merged.
Evidence and the two-phase plan: [followup-plan.md](followup-plan.md).

## 1. Phase 1 at a glance

The work has two phases. **Phase 1 changes only the server.** Phase 2 changes
only the CLI: one CLI release, with Skill revisions gated on it. Phase 1 has
three outcomes:

1. **Legacy scoring is measured as it is.** A score that a legacy CLI sends
   without an impression is attributed when the delivery facts make the
   exposure unique. Agents that pull but never score are counted.
2. **The server is ready for phase 2.** It accepts, stores, deduplicates and
   measures every field phase 2 CLIs will send.
3. **Nothing that exists today changes.** Released CLIs, ranking inputs, exact
   attribution and every current metric keep their meaning and values.

| Item | Repository | Content | Depends on | Migrations |
|---|---|---|---|---|
| PR 1 | eigenflux | Inferred attribution and silent Agents in compliance metrics | — | `000119` |
| O 1 | eigenflux-observability | Panels for PR 1 | PR 1 deployed | — |
| PR 2 | eigenflux | Feedback ingestion fields, idempotency, client health storage | — | `000120`, `000121` |
| PR 3 | eigenflux | Compliance metrics for PR 2 fields | PR 2 merged | `000122` |
| O 2 | eigenflux-observability | Panels for PR 3 | PR 3 deployed | — |

PR 1 and PR 2 can be built in parallel. Migration numbers follow merge order;
renumber if another migration lands first.

### Out of scope for phase 1

- CLI, Skills and host plugins.
- The legacy CLI upgrade announcement, which is handled separately.
- Ranking, recall, LR features and training data.
- Experiments. Phase 1 changes no behavior, so acceptance relies on tests,
  production read-only checks and before/after invariants (§3). There is no
  AA or AB test.

## 2. Invariants that every item must keep

| # | Invariant | Checked by |
|---|---|---|
| I1 | Released CLIs (any `0.0.x` and `0.6.x`) get the same responses, plus only new optional fields | E2E with old request bodies |
| I2 | `item_stats` counters, milestones and the high-quality counter change exactly as today for requests without the new fields | Consumer tests, E2E |
| I3 | `recommendation_effect_*`, `feedback_history_daily`, `stable_agent_daily` and the existing `feedback_compliance_daily` columns are unchanged for the same cutoff | Reconciliation tests, §3.3 |
| I4 | No fact row in `feedback_logs`, `followup_labels` or `replay_logs` is rewritten. Inferred attribution lives only in aggregates | Code review, SQL has no fact `UPDATE` |
| I5 | Grafana reads only `grafana_*` aggregate views without identifiers | Migration and view tests |
| I6 | Compliance refresh for the busiest retained day stays under 10 s; it is 8.0 s today, against a 15 s statement timeout | §3.2 |

## 3. Verification procedures used by several items

### 3.1 Local database tests

Use an isolated loopback database named `recommendation_metrics_*`:

```bash
RECOMMENDATION_METRICS_TEST_DSN="$LOCAL_METRICS_TEST_DSN" \
  go test -race -count=1 ./pkg/recommendationmetrics ./pipeline/cron \
  -run 'TestPostgresRecommendation|TestPostgresCompliance|TestCompliance|TestRecommendationEffect'
```

Without the variable, these PostgreSQL tests skip. `pipeline/cron` also needs
the local stack (`official_test.go`); that dependency predates this work.

### 3.2 Production read-only check

There is no read replica, so the check runs on the primary from `aliap` with
`psql` and the DSN in `/etc/eigenflux/runtime.env`. Never print the DSN.

- Wrap every statement in `BEGIN READ ONLY; SET LOCAL statement_timeout='20s';
  SET LOCAL lock_timeout='1s'; ... ROLLBACK;`.
- Rewrite the final `INSERT INTO feedback_compliance_daily (...) SELECT ...` as
  a CTE (`c_out`) followed by a `SELECT`. Fix the parameters as literals.
- Days and cutoffs:

  | Day | Purpose | Cutoff |
  |---|---|---|
  | 2026-10-08 | Busiest day (185,807 exposures) | 2026-10-11 00:03:50.128 +08 |
  | 2026-10-20 | Highest-coverage day | 2026-10-23 00:30 +08 |
  | 2026-10-04 | Audit day | 2026-10-07 00:30 +08 |

- Record `EXPLAIN (ANALYZE, BUFFERS)` timing for 10-08, and list sequential
  scans. Only `agents` and `agent_settings` are acceptable.
- Output aggregates only. Delete the uploaded SQL file from `aliap` afterwards.
- Attach the timings and results to the PR.

### 3.3 Post-deployment invariance

After each eigenflux PR is deployed and its days are backfilled:

- Before the backfill, export the `all` rows of the 30 days to be backfilled
  read-only, keeping only the pre-existing columns.
- After the backfill, the same columns must be identical for every final day
  (`snapshot_at >= day + 3`). Cohort rows may differ, because cohort values
  come from the settings snapshot at refresh time; the `all` row may not.
- `recommendation_effect_daily` rows for those days are not touched.

---

## 4. PR 1 — Inferred attribution and silent Agents

### 4.1 What changes

New definitions, documented beside the existing ones:

| Term | Definition |
|---|---|
| Impressionless score | A `feedback_logs` row from an external consumer (the `consumers` CTE exclusions) with `impression_id = ''` and `score IN (-1,0,1,2)` |
| Inferred score | An impressionless score with **exactly one** `replay_logs` candidate: same `agent_id` and `item_id`, `delivered IS TRUE`, `source_kind = 'broadcast'`, nonempty `impression_id`, `served_at` in `(feedback_at - 48h, feedback_at]`. Zero or several candidates stay unattributed |
| Inferred-scored exposure | A mature exposure without any valid exact score in its 48 h window that is the candidate of at least one inferred score. An exposure with an exact score counts as exact only |
| Silent Agent | A consumer with at least one delivered non-search exposure on day D and **no** valid score event of any kind in `[lo, min(hi + 48h, cutoff))` |

The window `(feedback_at - 48h, feedback_at]` is the exact-attribution
condition `served_at <= feedback_at < served_at + 48h`, rewritten from the
event's side.

New columns:

| Column | Basis | Meaning |
|---|---|---|
| `scored_48h_inferred_exposures` | Delivery day | Inferred-scored exposures |
| `unscored_batches_with_inferred` | Delivery day | Exact-unscored batches containing at least one inferred-scored exposure |
| `feedback_events_inferred` | Event day | Impressionless on-day score events that are inferred (search included, like the other event columns) |
| `silent_agents` | Delivery day | Silent Agents |

### 4.2 Migration `migrations/000119_feedback_compliance_inferred.sql`

This is a single transaction. Start it with `SET LOCAL lock_timeout = '5s'`.
The table is a small aggregate.

```sql
ALTER TABLE feedback_compliance_daily
  ADD COLUMN scored_48h_inferred_exposures BIGINT NOT NULL DEFAULT 0 CHECK (scored_48h_inferred_exposures >= 0),
  ADD COLUMN unscored_batches_with_inferred BIGINT NOT NULL DEFAULT 0 CHECK (unscored_batches_with_inferred >= 0),
  ADD COLUMN feedback_events_inferred BIGINT NOT NULL DEFAULT 0 CHECK (feedback_events_inferred >= 0),
  ADD COLUMN silent_agents BIGINT NOT NULL DEFAULT 0 CHECK (silent_agents >= 0),
  ADD CONSTRAINT chk_compliance_inferred_coverage CHECK (scored_48h_exposures + scored_48h_inferred_exposures <= mature_exposures),
  ADD CONSTRAINT chk_compliance_inferred_batches CHECK (unscored_batches_with_inferred <= unscored_batches),
  ADD CONSTRAINT chk_compliance_inferred_events CHECK (feedback_events_with_impression + feedback_events_inferred <= feedback_events),
  ADD CONSTRAINT chk_compliance_silent CHECK (silent_agents <= delivered_agents);
```

A `SELECT *` view keeps the column list it was created with, so rebuild the
view in the same migration:

1. `DROP VIEW grafana_feedback_compliance_daily`.
2. Recreate it `WITH (security_barrier=true)`.
3. `REVOKE ALL ... FROM PUBLIC`.
4. Run the conditional `GRANT ... TO grafana_ro_v2` `DO` block from `000118`.
5. Restore the `COMMENT`, adding one sentence: inferred counts are separate
   from exact and use a unique delivered exposure in the preceding 48 h.

Down rebuilds the view without the new columns, then drops the four columns
and four constraints.

### 4.3 SQL — `pkg/recommendationmetrics/compliance_day.sql`

Keep `bounds` … `exact_matches` byte-identical.
`TestComplianceClassificationContract` pins that prefix. After
`exact_matches`, make these changes:

1. **`legacy_scores` (MATERIALIZED).**
   - `consumers CROSS JOIN bounds CROSS JOIN LATERAL (SELECT id, item_id, feedback_at FROM feedback_logs f WHERE f.agent_id = c.agent_id AND f.feedback_at >= b.lo AND f.feedback_at < LEAST(b.hi + 172800000, b.cutoff) AND f.impression_id = '' AND f.score IN (-1,0,1,2) OFFSET 0)`.
   - Select `on_day = feedback_at < LEAST(hi, cutoff)`.
   - This uses `idx_feedback_logs_agent_feedback_at`.
2. **`inferred` (MATERIALIZED).** Per `legacy_scores` row:
   ```sql
   CROSS JOIN LATERAL (
     SELECT count(*) AS n, min(r.impression_id) AS impression_id
     FROM (SELECT agent_id, impression_id, delivered, source_kind FROM replay_logs r
           WHERE r.item_id = s.item_id
             AND r.served_at > s.feedback_at - 172800000 AND r.served_at <= s.feedback_at
           OFFSET 0) r
     WHERE r.agent_id = s.agent_id AND r.delivered IS TRUE
       AND r.source_kind = 'broadcast' AND r.impression_id <> ''
   ) x WHERE x.n = 1
   ```
   This uses `idx_replay_logs_item (item_id, served_at)`.
3. **`inferred_exposures` (MATERIALIZED).** `SELECT DISTINCT` the inferred
   `(agent_id, item_id, impression_id)` that exist in `mature_keys`. Search and
   ambiguous keys are excluded because `mature_keys` excludes them.
4. **`outcomes`.** Add `LEFT JOIN inferred_exposures i USING (agent_id, item_id, impression_id)`
   and select `i.agent_id IS NOT NULL AS inferred`.
5. **`exposure_counts`.** Add `count(*) FILTER (WHERE final_score IS NULL AND inferred)`.
   **`batches`:** add `bool_or(final_score IS NULL AND inferred) AS any_inferred`.
   **`batch_counts`:** add `count(*) FILTER (WHERE scored = 0 AND any_inferred)`.
6. **`inferred_event_counts`.** `SELECT agent_id, count(*) FROM inferred WHERE on_day GROUP BY agent_id`.
7. **`silent_counts`.** For `delivery_counts` Agents absent from
   `legacy_scores`, add `NOT EXISTS` on a fenced lookup of a valid score with a
   nonempty impression in the same window on `(agent_id, feedback_at)`, with
   `LIMIT 1`. Output `agent_id, 1`.
8. **Stack, never join.**
   - Add the four columns to every `agent_parts` branch, as zeros where a
     branch has no value.
   - Add `UNION ALL` branches for `inferred_event_counts` and `silent_counts`.
   - Extend `agent_totals`, `folded_counts`, and the INSERT column and SELECT
     lists.
   - Joining per-Agent aggregates is the regression the plan-shape test guards
     against.

If 10-08 exceeds 10 s, move steps 1–3 into a second statement in the same
transaction, using a temporary table created `ON COMMIT DROP` that the main
statement reads. Each statement then has its own 15 s timeout. Do not raise
timeouts.

### 4.4 Go, cron, backfill

No change. `RefreshComplianceDay`, `CompliancePendingDays`, the cron loop and
`--compliance` backfill pick up the new SQL. If §4.3's fallback is used,
`RefreshComplianceDay` executes the two statements in order inside its
existing transaction.

### 4.5 Scope

| Changed | Not changed |
|---|---|
| `migrations/000119_*.sql` | `refresh_day.sql`, `history_day.sql`, `stable_day.sql` |
| `pkg/recommendationmetrics/compliance_day.sql` | `compliance.go` (unless the §4.3 fallback is needed) |
| `pkg/recommendationmetrics/compliance_test.go` | Fact tables, ingestion, API, CLI, Skills |
| `docs/dev/recommendation_effect.md` | |
| `docs/design/feedback-compliance-baseline/design.md` (criterion 5) | |
| `deploy/release-events.json` | |

### 4.6 Acceptance

**Tests.** Use the helpers in `compliance_test.go`.
`complianceScore(t, c, agent, item, "", after, score)` writes an impressionless
score. Every case asserts the full `all` row with `assertComplianceCounts`,
which fails on any unexpected nonzero column, and ends with
`assertComplianceConservation`.

| # | Fixture | Expected (`all` row) |
|---|---|---|
| 1 | Exposure `(1,10,"a")` at `served`; impressionless score at `+5m` | `delivered=1, identifiable=1, mature=1, mature_batches=1, unscored_batches=1, scored_48h=0, scored_48h_inferred=1, unscored_batches_with_inferred=1, feedback_events=1, feedback_events_inferred=1, silent_agents=0` |
| 2 | Exposures `(1,10,"a")` at `served` and `(1,10,"b")` at `+1h`; impressionless score at `+2h` | `mature=2, unscored_batches=2, scored_48h_inferred=0, feedback_events=1, feedback_events_inferred=0` |
| 3 | Exposure at `served`; impressionless scores at `-1m` and `+48h` | `mature=1, unscored_batches=1, scored_48h_inferred=0, feedback_events=1` (the `-1m` event; `+48h` falls on a later day), `feedback_events_inferred=0, silent_agents=0` |
| 4 | Exposure with exact score at `+1m` and impressionless score at `+2m` | `scored_48h=1, scored_48h_inferred=0, fully_scored_batches=1, feedback_events=2, feedback_events_exact=1, feedback_events_inferred=1` |
| 5 | Exposure on 2026-10-01 23:50 +08; impressionless score 2026-10-02 00:05 | Day 10-02: `feedback_events_inferred=1`, `delivered=0`, `silent_agents=0`. Day 10-01 (cutoff 10-04 00:00): `scored_48h_inferred=1` |
| 6 | Search exposure; impressionless score at `+1m` | `mature=0, feedback_events_inferred=1` |
| 7 | Consumers 3, 5 and 6 (internal) with exposures and impressionless scores | All zero |
| 8 | Agent A: exposure, no score. Agent B: exposure, impressionless score at `hi + 47h` | `delivered_agents=2, silent_agents=1`. With B's score at `hi + 48h`: `silent_agents=2` |
| 9 | Case 1 plus `assertComplianceReconciliation` | All seven identities hold |
| 10 | `UPDATE`s violating each new CHECK | Every one fails |
| 11 | New `TestPostgresComplianceManyAgentsLegacyDay`: the 4,000-Agent fixture with scores where `id % 5 = 1` made impressionless | Plan discards < 1,000,000 rows. Values: `scored_48h=24000, scored_48h_inferred=8000, fully_scored_batches=4800, unscored_batches=3200, unscored_batches_with_inferred=1600, feedback_events=32000, feedback_events_with_impression=24000, feedback_events_inferred=8000, silent_agents=800` |

Case 11's numbers follow from the fixture: each Agent's batch shares `id % 5`.
Agents with `% 5 = 0` have no scores, `1` have impressionless scores, and the
rest have exact scores.

Also update `TestPostgresComplianceEventOnlyDayAndSchema` (row count, CHECK
list) and the view-column privacy check.

**Build.**

- `bash scripts/common/build.sh` succeeds.
- `go test ./pkg/recommendationmetrics ./migrations` passes without a DSN.
- §3.1 passes with a DSN.

**Production read-only (§3.2).**

- 10-20:
  - `feedback_events_inferred` is within 1% of 13,509, the standalone
    uniqueness probe for the same event day. Both include search exposures.
  - The seven identities and conservation hold.
- 10-08: under 10 s, with only the allowed sequential scans.
- For `cli_version` values `<= 0.0.55`, `(scored_48h + scored_48h_inferred) / mature`
  is above 0. For `0.6.x`, `scored_48h_inferred / mature` is below 2%.

**After deployment.**

- §3.3 holds for 30 backfilled days.
- The release annotation appears on the `feedback-compliance` page.

---

## 5. O 1 — Dashboard for PR 1

### 5.1 What changes

`scripts/generate_feedback_compliance_dashboard.py`:

- The `rate()` helper accepts a numerator expression; aggregate it as
  `sum(a + b)`. The per-denominator `>= 200` guard is unchanged.
- `RATES` gains:
  - `("48 h 覆盖率（含推断）", "scored_48h_exposures+scored_48h_inferred_exposures", "mature_exposures")`
  - `("推断归因打分事件占比", "feedback_events_inferred", "feedback_events")`
  - `("整批未打分中含推断占比", "unscored_batches_with_inferred", "unscored_batches")`
- The coverage trend (panel 2) gains the "含推断" line beside "48 h".
- **Silent Agent share.** `silent_agents / delivered_agents` is computed per
  day only, because distinct Agents cannot be summed across days. Show it as
  a new daily trend panel, and in the cohort table as the min and max of the
  daily ratio. The validator rejects `sum(delivered_agents)`; extend that rule
  to `sum(silent_agents)`.
- `DENOMINATOR_LABELS` gains `unscored_batches`. Also update
  `docs/feedback-compliance.md`: define inferred, state it is never added to
  exact elsewhere, and define silent.

### 5.2 Scope

Changed:

- `scripts/generate_feedback_compliance_dashboard.py`
- `grafana/dashboards/content/feedback-compliance.json` (regenerated)
- `scripts/test_feedback_compliance_dashboard.py`
- `scripts/validate_feedback_compliance_dashboard.py`
- `docs/feedback-compliance.md`

Not changed: other dashboards and the release-annotation scopes.

### 5.3 Acceptance

- The regenerated JSON matches the generator.
- `scripts/validate` passes with Python 3.14.
- New PostgreSQL tests:
  - Combined coverage equals `(Σ exact + Σ inferred) / Σ mature` over final days.
  - The silent share for each day equals `silent_agents / delivered_agents` for
    that day.
  - No query sums `delivered_agents` or `silent_agents`.
  - Each new rate is blank when its own denominator is below 200.
- After deployment, panels show data for the 30 backfilled days.

---

## 6. PR 2 — Feedback ingestion fields, idempotency, client health

### 6.1 What changes

**IDL (`idl/api.thrift`).**

```thrift
struct FeedbackItem {
    1: required string item_id (api.body="item_id")
    2: required i32 score (api.body="score")
    3: optional string impression_id (api.body="impression_id")
    4: optional string client_key (api.body="client_key")
    5: optional bool surfaced (api.body="surfaced")
    6: optional string impression_source (api.body="impression_source")
}

struct BatchFeedbackReq {
    1: required list<FeedbackItem> items (api.body="items")
    2: optional string impression_id (api.body="impression_id")
    3: optional string contract_version (api.body="contract_version")
}

struct FeedbackItemResult {
    1: required string item_id
    2: required string status
    3: optional string reason
}

struct BatchFeedbackData {
    1: required i32 processed_count
    2: required i32 skipped_count
    3: optional list<string> skipped_reasons
    4: optional list<FeedbackItemResult> results
}
```

Regenerate with `hz update -idl idl/api.thrift -module eigenflux_server`. Keep
the uppercase swagger annotations. `build.sh` regenerates swagger; commit only
the real changes.

**Handler `BatchFeedback` (`api/handler_gen/eigenflux/api/api_service.go:1189`).**
The same handler serves `/api/v1/items/feedback`, `/api/v2/feed/feedback` and
`/api/v2/items/feedback`.

- Move per-item validation into a pure function in the package, for example
  `normalizeFeedbackItem`. It returns either an `itemstats.Feedback` or a
  rejection reason.
- Validation (trim first). Item rejections keep the current
  `skipped_reasons` text style:

  | Field | Rule | On violation |
  |---|---|---|
  | `client_key` | Optional; 1–64 chars of `[A-Za-z0-9._:-]` | Item rejected: `invalid client_key for item <id>` |
  | `impression_source` | Optional; `explicit` or `cli_inferred`; requires a nonempty resolved impression | Item rejected: `invalid impression_source for item <id>` |
  | `surfaced` | Optional bool; absent stays absent; `false` is stored as false | — |
  | `contract_version` | Optional; 1–32 chars of `[A-Za-z0-9._/-]` | Whole request `400 invalid contract_version` |

- Existing rules are unchanged: invalid item ID, score outside −1..2, own item.
- `results` has one entry per input item, in input order:
  `{item_id, status: "accepted"}` or `{item_id, status: "rejected", reason}`.
  `processed_count`, `skipped_count` and `skipped_reasons` are unchanged.
- A publish failure still returns 500 after earlier items were published.
  State in the swagger description that a retry is safe only with
  `client_key`.
- `ConsoleHighlightFeedback` (`:3269`) publishes none of the new fields.

**Stream (`pkg/itemstats/events.go`).**

- Replace `PublishFeedback(ctx, agentID, itemID, score, impressionID)` with
  `PublishFeedback(ctx, Feedback)`:
  ```go
  type Feedback struct {
      AgentID, ItemID int64
      Score int
      ImpressionID, ClientKey, ImpressionSource, ContractVersion string
      Surfaced *bool
  }
  ```
  Update both callers.
- Stream fields `client_key`, `impression_source` and `contract_version` are
  written only when nonempty. `surfaced` is written as `"true"` or `"false"`
  only when non-nil.
- `Event` gains the same fields. `ParseEvent` reads them when present. Messages
  without them, including those already queued during deployment, parse
  exactly as today.

**Consumer and DAL (`pipeline/consumer/feedback_log_dal.go`).**

- `FeedbackLog` gains:
  - `ClientKey string` (`size:64;not null;default:''`)
  - `Surfaced *bool`
  - `ImpressionSource string` (`size:16;not null;default:''`)
  - `ContractVersion string` (`size:32;not null;default:''`)

  `newFeedbackLog` copies them.
- `insertFeedbackLog` uses `clause.OnConflict{DoNothing: true}` **without**
  `Columns`, which renders `ON CONFLICT DO NOTHING`. That covers both the
  `stream_message_id` unique index and the new `(agent_id, client_key)`
  partial unique index.
- The existing `!inserted` branch already skips `IncrementItemScore`, the
  high-quality counter and milestones. Do not change it.
- The server does not synthesize `followup_labels` from `surfaced`
  ([followup-plan.md](followup-plan.md), S3).

**Migration `migrations/000120_feedback_report_metadata.sql` (transactional,
`SET LOCAL lock_timeout = '5s'`).**

```sql
ALTER TABLE feedback_logs
  ADD COLUMN IF NOT EXISTS client_key VARCHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS surfaced BOOLEAN,
  ADD COLUMN IF NOT EXISTS impression_source VARCHAR(16) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS contract_version VARCHAR(32) NOT NULL DEFAULT '';

CREATE TABLE agent_feedback_health_daily (
  agent_id BIGINT NOT NULL,
  day DATE NOT NULL,
  reported_at BIGINT NOT NULL,
  window_ms BIGINT NOT NULL CHECK (window_ms BETWEEN 1 AND 172800000),
  pulls INTEGER NOT NULL CHECK (pulls BETWEEN 0 AND 1000000),
  delivered_items INTEGER NOT NULL CHECK (delivered_items BETWEEN 0 AND 1000000),
  scored_items INTEGER NOT NULL CHECK (scored_items BETWEEN 0 AND 1000000),
  status_runs INTEGER NOT NULL CHECK (status_runs BETWEEN 0 AND 1000000),
  feedback_attempts INTEGER NOT NULL CHECK (feedback_attempts BETWEEN 0 AND 1000000),
  feedback_failures INTEGER NOT NULL CHECK (feedback_failures BETWEEN 0 AND feedback_attempts),
  queue_drops INTEGER NOT NULL CHECK (queue_drops BETWEEN 0 AND 1000000),
  queue_depth INTEGER NOT NULL CHECK (queue_depth BETWEEN 0 AND 1000000),
  oldest_queued_age_ms BIGINT NOT NULL CHECK (oldest_queued_age_ms BETWEEN 0 AND 604800000),
  cli_version VARCHAR(32) NOT NULL DEFAULT '',
  PRIMARY KEY (agent_id, day)
);
REVOKE ALL ON agent_feedback_health_daily FROM PUBLIC;
```

- On PostgreSQL 18, adding a column with a constant default is metadata-only,
  with no table rewrite.
- The `feedback_logs` columns get no DB CHECK: values are validated at the API
  boundary, and a validated CHECK would scan 2.6M rows under lock.
- Neither table is granted to Grafana.
- Down drops the table and the four columns.

**Migration `migrations/000121_feedback_logs_client_key_index.sql`.** Copy the
structure of `000100_raw_items_author_content_md5.sql`:

- `-- +goose NO TRANSACTION`, `SET lock_timeout = '5s'`,
  `SET statement_timeout = '30min'`.
- `CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_feedback_logs_agent_client_key ON feedback_logs (agent_id, client_key) WHERE client_key <> '';`
- A verification `DO` block must check `indisvalid`, `indisready`,
  `indisunique`, `btree`, `indnkeyatts = 2`, the keys `agent_id` and
  `client_key`, and that `pg_get_expr(i.indpred, i.indrelid)` is the
  predicate. It raises with the repair instruction.
- Register `"uq_feedback_logs_agent_client_key"` in `knownConcurrentIndexes`
  in `scripts/common/migration_preflight.go`.
- Down: `DROP INDEX CONCURRENTLY IF EXISTS uq_feedback_logs_agent_client_key`.

**Client health (`PutMySettings`, `api_service.go:3424`).**

- `agentSettingsWriteRequest` gains
  `FeedbackHealth *feedbackHealthReport \`json:"feedback_health"\``. The
  fields are `window_ms`, `pulls`, `delivered_items`, `scored_items`,
  `status_runs`, `feedback_attempts`, `feedback_failures`, `queue_drops`,
  `queue_depth` and `oldest_queued_age_ms`. All are integers covering the
  trailing `window_ms` before the report.
- Validate with the table bounds. On failure return
  `400 invalid feedback_health`, before the transaction, as the handler already
  does for `lang`.
- Inside the existing transaction, after `ObserveRuntime` succeeds, upsert one
  row:
  - `day` is the Shanghai date of `requestStartedAt`.
  - `reported_at` is `requestStartedAt`.
  - `cli_version` is `clientInfo.CLIVer` truncated to 32.
  - On conflict: `ON CONFLICT (agent_id, day) DO UPDATE ... WHERE agent_feedback_health_daily.reported_at <= EXCLUDED.reported_at`.
- The DAL lives in a new `api/dal/feedback_health.go`.
- Absent `feedback_health` writes nothing.
- Add `{Table: "agent_feedback_health_daily", Where: "agent_id = $1"}` to
  `pkg/testaccountreset/reset.go`, next to `agent_settings`.

### 6.2 Scope

| Changed | Not changed |
|---|---|
| `idl/api.thrift`, hz-generated `api/model`, swagger | Other endpoints and routes, auth scopes |
| `api/handler_gen/eigenflux/api/api_service.go` (`BatchFeedback`, `ConsoleHighlightFeedback` call site, `PutMySettings`) | `PushFeedEvents`, follow-up ingestion |
| `api/dal/feedback_health.go` (new) | `agent_settings` schema |
| `pkg/itemstats/events.go` | Item stats semantics, milestones, high-quality counter |
| `pipeline/consumer/feedback_log_dal.go`, `item_stats_consumer.go` (only if needed for new fields) | `FollowupConsumer`, Swing surface history, replay ingestion |
| `migrations/000120_*.sql`, `000121_*.sql`, `scripts/common/migration_preflight.go`, `migrations/migrations_contract_test.go` | Existing `feedback_logs` data |
| `pkg/testaccountreset/reset.go` | CLI, Skills, plugins |
| `docs/dev/api_endpoints.md`, `docs/dev/pipeline.md` | Metrics (PR 3) |

### 6.3 Acceptance

**Tests.**

| Level | Test | Must assert |
|---|---|---|
| Unit, `api` | `normalizeFeedbackItem` table test | Every row of the validation table. `impression_source` without impression rejected. `surfaced=false` kept. Old item shapes unchanged |
| Unit, `api` | `feedbackHealthReport` validation | Each bound. `feedback_failures > feedback_attempts` rejected. Missing object is a no-op |
| Unit, `pkg/itemstats` (miniredis) | Publish/Parse round trip | All new fields round-trip. Absent fields stay empty or `nil`. A legacy message without the keys parses unchanged |
| Unit, `pipeline/consumer` (SQLite) | Create `CREATE UNIQUE INDEX ... WHERE client_key <> ''` with `gdb.Exec` after `AutoMigrate`. Two events, different message IDs, same `(agent_id, client_key)` | Second `inserted=false`. One row. `Score2Count` incremented once. Two events with empty `client_key` and different message IDs both insert |
| Unit, `pipeline/consumer` | Existing `TestPersistFeedbackEventDeduplicatesByStreamMessageID` | Still passes with the conflict clause change |
| E2E, `tests/e2e/feedback_test.go` | Old body | Response equals today's plus `results`. The row's new columns are `''` or `NULL` |
| E2E | Same `client_key` posted twice, sequentially and from two goroutines | One `feedback_logs` row. `item_stats` score counter incremented once. Fields stored exactly as sent |
| E2E | Mixed batch: valid, own item, invalid `client_key` | `results` in input order with statuses. `processed_count=1`, `skipped_count=2` |
| E2E | Batch with invalid `contract_version` | HTTP 400. Nothing published |
| E2E | Settings report with `feedback_health`; a second report in the same day; a replay with older `reported_at` | One row per Shanghai day, newest values win, older replay ignored |
| E2E | Existing feedback, milestone and settings tests | Pass unchanged (I1, I2) |
| Contract | `migrations_contract_test.go` | `000121` contains the required concurrent-index and verification strings. Preflight registers the index |

Run `./tests/run.sh`, the unit packages with `-race`, and
`bash scripts/common/build.sh`.

**After deployment.**

- `\d feedback_logs` shows `uq_feedback_logs_agent_client_key` as valid, and
  the `000121` verification block did not raise.
- For 48 h, the `item:stats` stream consumer metrics
  (`ConsumerMessagesTotal` failure status, `ConsumerRetryTotal`) stay at their
  pre-deployment level.
- Run a read-only check for the 24 h after deployment: no `feedback_logs` row
  has a nonempty `client_key`, `impression_source` or `contract_version`, or a
  non-NULL `surfaced`. No released CLI sends them, so any such row points to an
  unexpected client.
- Daily `feedback_events` for the two days after deployment are within the
  range of the previous seven days (I2, I3).

---

## 7. PR 3 — Compliance metrics for PR 2 fields

### 7.1 What changes

New columns (`NOT NULL DEFAULT 0`, `>= 0`):

| Column | Basis | Definition |
|---|---|---|
| `feedback_events_keyed` | Event day | Valid on-day score events with nonempty `client_key` |
| `feedback_events_cli_inferred` | Event day | Exact on-day score events with `impression_source = 'cli_inferred'` |
| `surface_declared_known_exposures` | Delivery day | Mature exposures whose final valid score has `surfaced IS NOT NULL` |
| `surface_declared_exposures` | Delivery day | Final score has `surfaced = true` |
| `surface_agreement_exposures` | Delivery day | Declared known, and the declared value equals the exposure's reported-surface flag (exact `surface` follow-up in the 48 h window) |
| `health_reported_agents` | Delivery day | Delivered Agents with an `agent_feedback_health_daily` row for day D |
| `silent_health_reported_agents` | Delivery day | Silent Agents (PR 1) with a row for D |
| `silent_attempting_agents` | Delivery day | Silent Agents whose row for D has `feedback_attempts > 0` |
| `health_feedback_attempts`, `health_feedback_failures`, `health_queue_drops` | Delivery day | Sums over delivered Agents' rows for D |

Constraints:

- `feedback_events_keyed <= feedback_events`
- `feedback_events_cli_inferred <= feedback_events_exact`
- `surface_declared_exposures <= surface_declared_known_exposures`
- `surface_agreement_exposures <= surface_declared_known_exposures`
- `surface_declared_known_exposures <= scored_48h_exposures`
- `health_reported_agents <= delivered_agents`
- `silent_attempting_agents <= silent_health_reported_agents`
- `silent_health_reported_agents <= silent_agents`
- `health_feedback_failures <= health_feedback_attempts`

New dimension `feedback_contract_version`:

- The value is the `contract_version` of the consumer's latest valid score
  event in `[lo, min(hi + 48h, cutoff))`.
- An empty version becomes `legacy`; an Agent with no score event becomes
  `none`.
- Values are bounded to 32 characters, with the usual five-Agent folding.

### 7.2 Migration `migrations/000122_feedback_compliance_reporting.sql`

- Add the columns and constraints. Replace the dimension CHECK: drop
  `feedback_compliance_daily_dimension_check` (verify the generated name with
  `\d`), then add a named one with the nine dimensions.
- Rebuild the view as in §4.2.
- Down reverses these steps. Rows with the new dimension must be deleted
  before the old CHECK is restored.

### 7.3 SQL

- In the `outcomes` score LATERAL, select `surfaced` in the fenced subquery and
  add `(array_agg(f.surfaced ORDER BY f.feedback_at DESC, f.id DESC))[1] AS declared_surfaced`.
  Use the name `declared_surfaced`, because `surfaced` already means the
  reported follow-up flag in `outcomes`.
- Keyed and CLI-inferred counts come from the on-day `feedback` CTE (it
  selects `f.*`) joined to `exact_matches` as today.
- Health: a CTE over `agent_feedback_health_daily WHERE day = $1::date`. Join
  it to `delivery_counts` and the PR 1 silent set in a new `agent_parts`
  branch.
- Cohort: add `('feedback_contract_version', ...)` to the `cohorts` `VALUES`.
  Fill it from a per-consumer fenced lookup on `(agent_id, feedback_at)` with
  `ORDER BY feedback_at DESC, id DESC LIMIT 1`, only for consumers present in
  `delivery_counts` or `feedback`.

### 7.4 Scope

| Changed | Not changed |
|---|---|
| `migrations/000122_*.sql` | Ingestion (PR 2), `refresh_day.sql` |
| `pkg/recommendationmetrics/compliance_day.sql`, `compliance_test.go`, fixture DDL in `refresh_test.go` | Shared attribution prefix |
| `docs/dev/recommendation_effect.md`, `deploy/release-events.json` | CLI, Skills |

### 7.5 Acceptance

**Tests.** Extend the fixture `feedback_logs` with the PR 2 columns and add
`agent_feedback_health_daily`.

| # | Fixture | Expected |
|---|---|---|
| 1 | Final score `surfaced=true` plus a reported surface | `known=1, declared=1, agreement=1` |
| 2 | Final score `surfaced=false` plus a reported surface | `known=1, declared=0, agreement=0` |
| 3 | `surfaced` absent | `known=0` |
| 4 | Earlier `surfaced=true`, later score without `surfaced` | `known=0` (the final score wins) |
| 5 | Scores with `client_key`, with `impression_source='cli_inferred'`, and one inferred but not exact | `keyed` and `cli_inferred` count exactly those events. `cli_inferred` counts only exact events |
| 6 | Silent Agent with a health row `feedback_attempts=3, failures=1`; another Agent's row for a different day | `silent_health_reported_agents=1, silent_attempting_agents=1, health_feedback_attempts=3, health_feedback_failures=1`. The other day is ignored |
| 7 | Agents with contract versions `''`, none, and `v2` (five Agents each), plus one `v3` Agent | Rows `legacy`, `none`, `v2`, and `other` containing `v3`. Conservation holds for the new dimension |
| 8 | CHECK violations for each new constraint and an unknown dimension | Every one fails |
| 9 | Plan-shape test (PR 1 case 11) | Still < 1,000,000 discarded rows |

**Production read-only (§3.2).**

- On 10-20, every new column is 0. This is expected: no client sends the
  fields yet.
- `feedback_contract_version` has only `legacy` and `none`.
- The seven identities and conservation hold, and 10-08 is under 10 s.

**After deployment.**

- §3.3 holds.
- Synthetic end-to-end check in the local stack: post scores with all PR 2
  fields and a `feedback_health` report, refresh the day, and confirm every
  new column counts them.

---

## 8. O 2 — Dashboard for PR 3

### 8.1 What changes

- `DIMENSIONS` gains `feedback_contract_version`. The validator rule changes
  from eight to nine dimensions.
- New rates, each guarded by its own denominator:

  | Label | Numerator / denominator |
  |---|---|
  | 声明展示一致率 | `surface_agreement_exposures / surface_declared_known_exposures` |
  | 声明展示覆盖率 | `surface_declared_known_exposures / scored_48h_exposures` |
  | 幂等键覆盖率 | `feedback_events_keyed / feedback_events` |
  | CLI 推断 impression 占比 | `feedback_events_cli_inferred / feedback_events_exact` |
  | 客户端打分失败率 | `health_feedback_failures / health_feedback_attempts` |

- The silent-Agent panel gains two daily series: silent Agents with a health
  report, and silent Agents that attempted scoring. Agent counts are never
  summed across days.
- Docs and tests are updated in the same way as O 1.

### 8.2 Scope

The same files as O 1.

### 8.3 Acceptance

- `scripts/validate` passes.
- Tests cover each new rate's guard and the new dimension option.
- After deployment, panels render and show zeros until phase 2 clients
  report.

---

## 9. Rollout and rollback

| Step | Action | Rollback |
|---|---|---|
| 1 | Deploy PR 1. Export §3.3 baselines. Backfill 30 days with `./build/recommendation_effect_backfill --compliance --days=...`. Check §3.3 | Revert code; Down `000119` (view rebuild, columns dropped) |
| 2 | Merge O 1 | Revert dashboard |
| 3 | Deploy PR 2. `000121` builds concurrently during deployment; confirm the index is valid | Revert API and consumer code; the additive columns, table and index stay, and old code ignores them |
| 4 | Deploy PR 3, backfill 30 days, check §3.3 | Revert code; Down `000122` |
| 5 | Merge O 2 | Revert dashboard |

## 10. Phase 1 acceptance summary

| Outcome | Accepted when |
|---|---|
| Legacy scoring measured | PR 1 production checks pass. Combined 48 h coverage per `cli_version` is visible and legacy versions are above 0% |
| Server ready for phase 2 | The PR 3 synthetic end-to-end check passes: all PR 2 fields are stored, deduplicated and counted |
| Nothing existing changed | Invariants I1–I6 hold. §3.3 holds after every backfill. Ingestion metrics stay at baseline 48 h after PR 2 |
