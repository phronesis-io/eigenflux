# Feedback compliance verification

## Verified locally

- Full core build: `bash scripts/common/build.sh`.
- Isolated local stack startup and all migrations through `000118`.
- `go test -race -count=1 ./pkg/recommendationmetrics ./pipeline/cron ./scripts/recommendation_effect_backfill`
  with an explicit loopback `recommendation_metrics_*` PostgreSQL database.
- The same affected packages and `./migrations` pass without a metrics DSN;
  PostgreSQL fixture cases skip explicitly in that mode.
- Ten native compliance cases plus the shared classification contract cover
  exact counts, all seven reconciliation identities, every additive count's
  conservation, anonymity thresholds, timing boundaries, final-score ordering,
  PM initiation, writers and schema constraints.
- Live local E2E: `TestPushFeedEvents`, `TestRuntimeCLIVersionReported` and
  `TestSettingsRampUserSetSemantics`.
- A rolled-back local transaction created `grafana_ro_v2` before applying the
  migration: it could read the view and could not read the aggregate base
  table or the synthetic fact table. No test role or schema was retained.
- Release-annotation unit tests, deployment-hook tests and API-hook tests.
- Observability `scripts/validate` with Python 3.14, including all compliance
  panel queries and the non-final-day annotation on PostgreSQL 16. System
  Python 3.9 fails existing unrelated PGC provenance hashes; those same tests
  pass with the installed Python 3.14 interpreter.

The SQL field classification prefix is identical to recommendation effect.
Event-side exact attribution includes search, while delivery coverage excludes
it. Long normalized model values are truncated to the aggregate's 64-character
limit. Cohort tables show daily Agent minimum/maximum instead of adding distinct
Agents across days.

## Outstanding acceptance

- The complete pipeline integration suite was attempted with no API credentials
  and loopback-only provider endpoints. `TestEmbedding` fails because no local
  embedding provider is running; LLM-dependent cases skip. External provider
  credential reuse was rejected by automatic approval review and was not used.
  No end-to-end LLM publication flow is claimed as verified.
- Production read-replica access is not configured for this task. The busiest
  retained day's `EXPLAIN ANALYZE` (<10 seconds, no fact-table sequential scans),
  same-cutoff production reconciliation, conservation, and the October 4 audit
  plausibility check remain rollout gates.
- Production grants, healthy deployment annotations, the 30-day backfill and
  populated Grafana panels require the reviewed main-branch deployment. No
  production data or services were modified.
