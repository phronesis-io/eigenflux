# Discovery process E2E tests

This opt-in suite starts the built API, Feed, Sort and Item processes, discovers
RPC services through etcd, and calls the authenticated V2 HTTP endpoints. It uses
real PostgreSQL, Redis and Elasticsearch. A production replay consumer runs in
the test process and persists delivered samples from the Redis stream into the
existing `replay_logs` table.

## Run

Provide a disposable, migrated local stack through `.env` or exported settings:
`PG_DSN`, `REDIS_ADDR`, `REDIS_PASSWORD`, `ES_URL`, `ETCD_ADDR`, and the matching
`EMBEDDING_DIMENSIONS`. Apply migrations through 106. No application services may
be registered in that etcd instance; the suite owns application startup and
shutdown. Do not run it against production or concurrently with other suites
using the same infrastructure.

```sh
bash scripts/common/build.sh
DISCOVERY_E2E=1 ./tests/run.sh --skip-start discoverye2e -count=1
```

The runner supplies `APP_ENV=test`. Without `DISCOVERY_E2E=1`, the suite skips.
The suite enables the new pipeline in its child processes, uses dynamic ports,
and waits for both RPC registration and HTTP readiness. Process logs and test
taxonomy/rule assets are saved in `build/discovery-e2e-<fixture-id>/`.

Fixture accounts and rows use unique IDs larger than JavaScript's safe integer
range. Cleanup removes owned rows, Redis keys and ES documents/indices without
flushing shared stores. The broadcast fixture uses the existing item index;
commission and Agent fixtures use separate, uniquely named indices. Existing
integration suites' PostgreSQL advisory lock is respected.

## Coverage

- Need JSON normalization, public Card language defaults, durable create
  idempotency, owner/scope checks, optimistic revision conflicts and lifecycle.
- Three-kind search through HTTP → Feed → Sort, typed IDs and private-data
  exclusion, response idempotency and mismatched-payload rejection.
- Exact Agent lookup by long ID, case-sensitive short ID, current name and
  English name, including Agents absent from ES, duplicate names, overflowing
  IDs, current-name previews, and self/block/language exclusions.
- Hard price, duration, region and language filters; inline Needs do not become
  saved Needs. Known zero prices satisfy a zero budget; missing region evidence
  does not satisfy a region constraint.
- Known Agents remain searchable but are excluded from recommendations. Omitted
  Need IDs select an active saved Need.
- Recommendations for each kind, eventual history writes, repeat suppression,
  and frozen idempotent responses after a Need closes.
- Search history is separate from automatic history; automatic exposures do not
  prevent later explicit searches.
- Active constrained Needs never broaden when no candidate matches. Changes to
  block relations affect fresh requests while cached responses remain frozen.
- Delivered sample consumption, Need/revision provenance, new pipeline/schema
  markers, nullable broadcast IDs for other kinds, and legacy event compatibility
  in the same replay table. Checks poll eventual state rather than requiring an
  atomic delivery/history/sample transaction.
- Redis forward features drive recorded scores; statistics-only updates leave ES
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
