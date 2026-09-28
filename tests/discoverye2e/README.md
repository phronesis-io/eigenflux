# Discovery process E2E tests

This opt-in suite starts the built API, Feed, Sort and Item processes, discovers
RPC services through etcd, and calls the authenticated V2 HTTP endpoints. It uses
real PostgreSQL, Redis and Elasticsearch. A production replay consumer runs in
the test process and persists delivered samples from the Redis stream into the
existing `replay_logs` table.

## Run

Provide a disposable, migrated local stack through `.env` or exported settings:
`PG_DSN`, `REDIS_ADDR`, `REDIS_PASSWORD`, `ES_URL`, `ETCD_ADDR`, and the matching
`EMBEDDING_DIMENSIONS`. Apply migrations through 108. No application services may
be registered in that etcd instance; the suite owns application startup and
shutdown. Do not run it against production or concurrently with other suites
using the same infrastructure.

```sh
bash scripts/common/build.sh
DISCOVERY_E2E=1 ./tests/run.sh --skip-start discoverye2e -count=1
```

To include the actual CLI process, build it from `cli/` with
`go build -o ../build/cli/eigenflux-needs-linked .`, then add
`EIGENFLUX_TEST_CLI="$PWD/build/cli/eigenflux-needs-linked"` to the runner command.
The CLI case verifies query search with filters, automatic selection of a captured
Need without caller-supplied IDs, cursor pagination across all three kinds,
batch recommendation limits without padding, and idempotent retries.

The runner supplies `APP_ENV=test`. Without `DISCOVERY_E2E=1`, the suite skips.
The suite enables the new pipeline in its child processes, uses dynamic ports,
and waits for both RPC registration and HTTP readiness. Process logs and test
rule assets are saved in `build/discovery-e2e-<fixture-id>/`.

Fixture accounts and rows use unique IDs larger than JavaScript's safe integer
range. Cleanup removes owned rows, Redis keys and ES documents/indices without
flushing shared stores. The broadcast fixture uses the existing item index;
commission and Agent fixtures use separate, uniquely named indices. Existing
integration suites' PostgreSQL advisory lock is respected.

## Coverage

- Missing Agent/service context returns HTTP 200 with empty items. All-empty
  discovery still permits a complete Feed response with context delivery, cadence
  and notification fields.

- Real Need Capture HTTP → current input → search/recommendation, owner/kind
  boundaries, standard language codes, original input/Intent provenance in
  execution snapshots and samples, Intent edits, expired deadlines, successful
  search/recommendation with open requirements, multi-Need delivery, and frozen
  retries.
- Maximum-length Chinese Intent fallback preserves valid captured Needs for other
  kinds. A stalled embedding provider times out within its optional budget while
  lexical results still return through the complete RPC chain.
- Frozen search cursor pages, owner/request binding, page retries, and absolute
  positions in delivered samples under the same impression.
- Three-kind search through HTTP → Feed → Sort, typed IDs and private-data
  exclusion, response idempotency and mismatched-payload rejection.
- Exact Commission lookup through the unified API/CLI and compatibility facade,
  including filters, missing IDs, retry caching, new-pipeline samples and no model calls.
- Exact Agent lookup by long ID, case-sensitive short ID, current name and
  English name, including per-kind exact-hit priority, page-local type blocks and frozen pages,
  Agents absent from ES, duplicate names, overflowing
  IDs, current-name previews, and self/block/language exclusions.
- Shared query processing for captured/inline Needs across all three kinds,
  including lexical matching with pending vectors and original Need provenance.
- Full-width Latin, Chinese and mixed-script
  queries can match English-only fixtures through the dense channel; failed embedding has no alias fallback. Query analysis
  persists in the existing samples; language constraints stay hard. A deterministic
  embedding outage verifies no match without lexical evidence or alias expansion.
- Hard price, duration, region and language filters; inline Needs do not become
  saved Needs. Known zero prices satisfy a zero budget; missing region evidence
  does not satisfy a region constraint.
- Known Agents remain searchable but are excluded from recommendations. Omitted
  Need IDs select an active saved Need.
- Recommendations for each kind, eventual history writes, repeat suppression,
  and frozen idempotent responses after a linked Intent becomes inactive.
- Search history is separate from automatic history; automatic exposures do not
  prevent later explicit searches.
- Active constrained Needs never broaden when no candidate matches. Changes to
  block relations affect fresh requests while cached responses remain frozen.
- Delivered sample consumption, NeedInput/projection/Intent provenance, new pipeline/schema
  markers, nullable broadcast IDs for other kinds, and legacy event compatibility
  in the same replay table. Checks poll eventual state rather than requiring an
  atomic delivery/history/sample transaction.
- All-kind Redis forward features drive recorded scores; warm broadcast features
  cannot bypass current item state. Statistics-only updates leave ES
  unchanged. ES documents exclude ranking-only fields. Missing/mismatched
  projections skip candidates, corrupt Redis types error, and catalogue RPC
  failures do not affect online ranking.

## Boundaries

Embedding uses a deterministic local HTTP fixture; commission catalogue and
order statistics use deterministic Kitex servers with real RPC transport.
Authentication sessions, public Cards, processed broadcasts and index documents
are seeded directly. This suite validates online serving, not onboarding,
asynchronous content processing, index refresh scheduling, semantic quality,
the remote commission deployment, model training or production load targets.
The replay consumer is real but runs in the test process instead of launching the
entire asynchronous pipeline.

The cold-start regression verifies that a constrained broadcast Need cannot
broaden its own route while Agent and commission routes use owner-context
fallback. It waits for asynchronous history writes and removes only its own
typed exposure entries before later deduplication cases. Capture maintenance
HTTP/CLI, concurrent retries and transaction rollback are exercised in
`tests/needs` against PostgreSQL migrated through 000109.

The Need vector case verifies capture makes no model call, cold online execution
returns lexical matches with `embedding_pending`, and the real background worker
warms Redis through the deterministic embedding HTTP fixture. Repeated fresh
online requests then include semantic recall while the fixture observes exactly
one model call for that processed text. Infrastructure must be migrated through
000110. Retry, lease recovery, cache eviction and model-generation isolation also
run in `tests/needs` with isolated job schemas and real Redis/PostgreSQL.

Context cache coverage exercises warm empty-input caches followed by real HTTP
Need creation, batch capture and Intent mutation, plus an actual Card rebuild.
It checks independent execution IDs, deadline/constraint behavior, cache-free
sample replay and zero new `discovery_contexts` rows. Direct SQL fixture changes
explicitly invoke the post-commit invalidation helper; writer-hook assertions use
production write paths.

`TestFeatureYAMLReloadWithoutServiceRestart` validates external YAML field changes
in the running Sort process against persisted ranking samples.

Slim-forward regressions assert nonempty three-kind previews and preserve Latin
word-boundary/full-width exclusion behavior through the HTTP and RPC stack.
The Sort source integration suite additionally covers Chinese exclusions, exact
Agent exclusions, ID-only pool summaries/text, changed-content hash rejection,
and read-through repair of pre-upgrade broadcasts without content hashes.

`FeatureMetricsAreExposedBySort` scrapes the running Sort process and verifies
read outcomes, request-cache reuse and all four views, and verifies that removed
intermediate histograms are absent.
Feature unit tests count Redis commands/pipeline rounds, verify script-cache
recovery and independent version fences, and exercise real Redis retention audit.

Feature census tests cover physical key counts across generations/components,
zero counts after expiry, fenced resumable checkpoints, replica pacing, and
Prometheus exposition. Miss-rate tests exclude repeated request-cache reads and
Redis errors while including physical misses and logical expiry.
