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
