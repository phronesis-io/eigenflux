# Local validation

The main-branch capture path stores authoritative NeedInput v2 snapshots directly.
The code removes online normalization and vocabulary publication, preserves v1
capture/retry compatibility, and retains historical projection tables. The
separate `codex/need-search-mvp` branch is unchanged.

## Verified

- All 12 core service binaries and the native CLI build.
- All CLI module tests.
- Need contract unit tests, including v1 compatibility, v2 schema parity, weighted
  field limits, mandatory/preferred condition separation, standard codes and quotes.
- Race tests for `pkg/need`, `api/consolev2`, and `api/middleware` with local PostgreSQL.
- Need integration tests against the real gateway and PostgreSQL, including CLI
  create/retry/get/list using the published v2 contract example.
- Migration 105→106 preserves historical input JSON, retry hash, timestamps and
  projections. Safe downgrade/reapply succeeds; downgrade with v2 input is rejected
  without losing data. Intent changes invalidate direct input eligibility.
- Focused gateway/pipeline e2e tests: `TestPushFeedEvents` and
  `TestSettingsRampUserSetSemantics`.
- Go vet for Need, its integration tests and the gateway package; `git diff --check`.

## Environment limit

The broad e2e suite requires configured external LLM credentials for profile
processing. The isolated stack has no credentials; the provider returns HTTP 401
and profile processing exhausts retries. `TestFeedbackFlow` fails waiting for
profiles; the broad run reaches its three-minute limit in `TestE2EFullFlow`.
This does not affect Need capture, which
has no model dependency. No fallback behavior or production credentials were added.

Apply migration 000106 before running the new gateway. Retain the historical
normalization tables/view until downstream readers have migrated. Skills use v2;
coordinate their publication with backend rollout. No production deployment or
changes to the separate discovery branch are included.
