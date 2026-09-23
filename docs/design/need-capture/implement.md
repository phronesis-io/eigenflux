# Local implementation validation

Implemented locally on `codex/need-capture`: Intent-linked input capture, synchronous
basic normalization, current Intent version reads, versioned projection storage,
coverage metadata, atomic offline enrichment publication, Agent guidance, and tests.
Vocabulary construction, enrichment scheduling, and retrieval integration remain
separate work. No production operations are part of this increment.

## Online independence contract

- Every successful create commits a `normalized` input and a `basic.v1` projection
  together, with no taxonomy/model/embedding/queue dependency.
- `unmapped`, `partial`, and `mapped` are vocabulary coverage states. All can be
  eligible; coverage is not a prerequisite for online use.
- Source text, phrases, and explicit constraints are retained. Unknown constraint
  alternatives are preserved without applying a narrower hard filter.
- Offline computation precedes its publication transaction. CAS updates only
  projection rows; a failed publication rolls back to the previous active result.
- Normal reads use committed snapshots and do not wait for offline row locks.
  Online retries preserve an enriched result; old pending inputs can receive a
  basic result on retry without changing their input ID.

## Passed (2026-09-23)

- `bash scripts/common/build.sh`: all 12 core binaries compiled.
- `./scripts/local/start_local.sh`: migration 105 and all local services started
  on the isolated `ef-need-capture-test` stack. The gateway was also restarted
  with Console V2/control enabled for actual gateway tests.
- `go test ./pkg/need ./api/consolev2 ./api/middleware`: passed.
- `go test -race ./pkg/need`: passed. Covers missing/empty/incomplete vocabulary,
  aliases, unresolved alternatives, source preservation, zero-valued constraints,
  description length boundaries, removed-field rejection, many-to-one mappings,
  invalid inputs/vocabulary, and normalized JSON Schema parity.
- `./tests/run.sh --skip-start needs` with the native CLI in
  `EIGENFLUX_TEST_CLI`: all ten PostgreSQL/HTTP/CLI tests passed.
- The same ten tests with `NEED_TEST_API_URL=http://127.0.0.1:18083` passed
  against the actual gateway and local stack. Tests verify immediate baseline
  availability, stalled/failed offline publication isolation, atomic rollback,
  partial/full coverage, immutable taxonomy versions, concurrent publisher CAS,
  retry preservation, legacy pending upgrade, ownership and Intent invalidation.
  Revised fields are validated across HTTP/CLI. Three retained projections remain
  linked to the unchanged input and Intent snapshot after the Intent advances.
  The CLI test submits the complete Skill JSON example and verifies its CNY
  budget survives normalization. Unit/schema/HTTP cases reject unsupported or
  explicitly empty currencies; CNY amounts remain integer fen.
- `./tests/run.sh --skip-start e2e --case
  'Test(PushFeedEvents|SettingsRampUserSetSemantics)$'`: both passed.
- `go vet ./pkg/need ./api/consolev2 ./api/middleware ./tests/needs`: passed.
- All CLI module tests and the native CLI build: passed.
- `git diff --check`: passed.
- Consolidated migration 105: up from 104, down to 104 (both tables and the
  view removed), and up again passed. All ten Need integration tests passed
  against the consolidated schema.

Earlier capture validation also passed the complete PostgreSQL Console V2 suite,
auth suite, native CLI build, and all CLI module tests. The broader failures below
were recorded then; this increment does not change those paths or rerun tests
whose external prerequisites are still absent.

The isolated test services/containers are stopped after validation. Test-created
NeedInput and Normalized Need rows are removed; the local containers remain
available for inspection and restart.

## Local execution constraint

The execution tool reclaims background service processes when their parent session
is collected. An initial separate-session gateway/E2E attempt failed with connection
refused. Running startup and both test suites within one session passed, without
application changes or test fallbacks.

## Broader regression limits

The full existing E2E/website suites are not green in this local configuration.
The initial Feed/feedback selection was stopped while waiting for profile LLM
processing. `TestGetItemErrorCases`, `TestWebsiteStatsIncrement`, and
`TestWebsiteStatsHighQuality` failed because item processing requires configured
LLM authentication; logs show an invalid authentication header and strict-mode
item discard. `TestLatestItemsPush` consequently found no processed items.

`TestRuntimeCLIVersionReported` failed its legacy `client_host`/`mode` assertion:
the response reported `runtime_name=openclaw` and `runtime_version=1.2.3` but empty
legacy fields. This change does not modify that reporting path. No production
credentials were copied, no fallback was added, and these failures are not counted
as passes. CLI release signing/cross-platform publishing was not performed.
