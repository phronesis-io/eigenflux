# Phase 1 verification

## Inferred attribution and silent consumers

Baseline: `06387f87`. Local PostgreSQL regression tests pass with `-race`,
including exact attribution, half-open windows, search events, ambiguous
candidates, cross-day inference, internal consumers, silent consumers, schema
checks, cohort conservation, and seven recommendation-effect reconciliations.

The unchanged 4,000-Agent / 40,000-delivery fixture includes 8,000
impressionless scores. Its plan discards 314,421 rows (limit: 1,000,000), with
24,000 exact and 8,000 inferred scored exposures and 800 silent consumers.
The refresh took 2.65 s locally.

The specified inference join produced a quadratic nested loop locally. The
implementation uses a hashed tuple membership test for inferred exposures and
puts the Agent predicate inside the bounded replay lookup. Definitions and the
shared exact-attribution prefix are unchanged; no new fact index is required.

Production checks used read-only transactions, a 20 s statement timeout and a
1 s lock timeout. SQL was streamed to the client; no source or query files were
written on the deployment host. Only aggregate results were returned.

| Shanghai day | Fixed cutoff (+08) | Inferred score events | Inferred scored exposures | Silent consumers | Changed baseline fields | Conservation mismatches |
|---|---|---:|---:|---:|---:|---:|
| 2026-10-08 | 2026-10-11 00:03:50.128 | 29,321 | 28,905 | 173 | 0 | 0 |
| 2026-10-20 | 2026-10-23 00:30:00 | 13,509 | 13,192 | 202 | 0 | 0 |
| 2026-10-04 | 2026-10-07 00:30:00 | 4,334 | 4,282 | 171 | 0 | 0 |

All pre-existing fields match the baseline query for every cohort at each
cutoff. On 10-08, `EXPLAIN (ANALYZE, BUFFERS)` took 9,680.908 ms; sequential
scans were limited to `agents` and `agent_settings`.

On 10-20, inferred events exactly match the planned 13,509. All retained
0.0.x cohorts through 0.0.55 have nonzero combined coverage. The 0.6.3–0.6.8
cohorts have no inferred events; the refresh-time 0.6.9 cohort has 3.03%, above
the plan's 2% plausibility expectation. Historical events use current client
settings, so this is recorded as an observation rather than changing attribution.

Post-deployment backfill comparisons remain a release check: no deployment or
production backfill was performed during development.

## Feedback ingestion and health storage

The full core build passes. Affected API handler, API DAL, item-stats, consumer,
account-reset and migration unit packages pass with `-race`. The local stack
applied migrations 000120–000121 successfully, including concurrent-index
verification. End-to-end tests pass for old bodies and defaults, ordered mixed
results, invalid-contract rejection without publishing, sequential and concurrent
keyed retries, exact metadata persistence, once-only score counters, health
ordering and Shanghai bucketing. Existing follow-up, CLI-version and settings-ramp
end-to-end tests also pass.

These tests seed completed items and exercise the actual API/Redis/consumer/DB
path. LLM-dependent publication and profile-enrichment suites were not run;
no model-provider credentials were used. Production ingestion and post-rollout
24/48-hour checks remain deployment acceptance steps.

## Reporting metrics and dashboards

All 11 reporting columns are covered by the real API/consumer end-to-end test,
including final-score surface declaration/agreement, keyed and CLI-inferred
exact events, daily health counters and silent-consumer health/attempts. The
metrics package passes with `-race`, including 4,000-Agent plan shape, new
constraints, nine-dimension conservation, contract folding and migration Down
checks. The full cron package passes with `-race`; metrics tests without a DSN
skip PostgreSQL fixtures and pass. `go vet` passes for affected packages.

A production read-only preview projects the migration's default feedback values
and empty health table through non-materialized CTEs; it does not deploy schema.
The conservative preview retains per-event aggregation using nonconstant default
expressions. October 8 took 8,292.817 ms with sequential scans only on Agents and
settings. October 20 has zero in all 11 new columns, `legacy` (435 delivered
Agents) and `none` (202) contract cohorts, unchanged pre-existing `all` counts,
and no conservation mismatches. Actual post-deployment timings and invariance
checks remain rollout gates.

Both dashboard stages preserve per-denominator guards and avoid summing distinct
Agents across dates. Full observability `scripts/validate` passes with Python
3.14, including all 12 feedback-compliance PostgreSQL/query contract tests.
No services were deployed to production and no historical facts were rewritten.
