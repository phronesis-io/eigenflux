# Implementation Tracking

Branch: `codex/need-search-mvp`, based on `340417c6`.
Implementation contract and operation: [Search and Recommendation MVP](../../dev/discovery.md).

## Scope delivered

- [x] Typed contracts, immutable taxonomy, query/Need/Agent-context compiler, hard-filter evaluator, independent rule scorers and fixtures.
- [x] PostgreSQL context migrations/storage, ownership, revision CAS, durable create idempotency and lifecycle limits.
- [x] Broadcast/commission/Agent retrieval, bounded authoritative hydration, public-only Agent projection and relation checks.
- [x] Sort/Feed RPC, existing-route adapters, legacy Feed pages, unified HTTP and CLI.
- [x] Existing replay stream/table with explicit generation/mode/schema, typed nullable identities, separate search history and exact CLI attribution.
- [x] Projection/maintenance integration, additive existing-index mappings, documentation and synchronized Skill guidance.
- [x] Build, local service start, affected unit/integration/E2E checks and review completed; model-dependent exceptions are recorded below.

## Validation evidence

- Core build and native CLI build passed.
- Affected domain, serving, API, source, configuration, projection and consumer unit tests passed.
- Race checks passed for discovery, serving, taxonomy, replay contracts and source/context packages.
- Vet passed for the new domain/adapter packages.
- Real PostgreSQL context tests passed: ownership, revision conflicts, durable idempotency and active limits.
- Real PostgreSQL/Elasticsearch/Redis tests passed: all three kinds, colliding numeric IDs across kinds, free commissions, repeated search impressions, frozen cache retries and current block filtering on new requests. Commission source RPC responses use a deterministic contract fixture.
- PostgreSQL replay migration/DAL tests passed: old/new row defaults, nullable nonbroadcast `item_id`, typed source IDs and consumer retry uniqueness.
- Legacy Sort and Auth integration suites passed.
- CLI input and exact-impression attribution tests passed.
- Full E2E passed (254.342 seconds); corrected Website suite passed (66.638 seconds).
- Final real-store integration rerun passed, including repeated/incompatible Agent index mapping checks.
- Final middleware tests passed for discovery activity, Need writes, metadata reads and failed requests.
- Delivery simplification regression checks passed: history/sample failures are independent, slow recording does not block return, request cancellation does not cancel background writes, cached responses do not revalidate, new requests observe closed Needs/blocks, missing page details are skipped, and page positions remain delivery-only.
- The simplified delivery code passed core build, race checks, vet, real PostgreSQL/ES/Redis integration, the Sort integration suite and full E2E.

The extended Pipeline suite is not green. Existing model-dependent keyword
extraction returned generic banned keywords and 13 terms where the fixture
allows at most 10. One existing placeholder-content fixture received literal
`{{title}}` instead of JSON. The tested prompt/LLM implementation is unchanged
by this task; these failures are recorded, not masked. Other distribution,
misjudged-content and safety fixtures completed, with the detailed local run in
`build/test-final-pipeline.log`.

Two stale regression assumptions were corrected: runtime mode now requires
explicit `X-Client-Mode`, verifies separate product fields, and preserves identity
when a legacy request lacks CLI metadata; Website HTTP checks
use `testutil.BaseURL` instead of a hardcoded 8080 instance.

## Implementation choices

- Use existing process boundaries, DB, ES cluster and Redis; no new deployed microservice.
- Keep frozen executable context in one JSONB record with a separate vector payload; no separate Need history product.
- Taxonomy lookup is bounded in memory. No additional compiled-plan or taxonomy-result cache is included. Agent-context fallback can embed up to five clauses per request; measure this path explicitly before accepting the latency gate.
- Agent projection uses configured versioned index `agent_discovery_v1`; its mapping is checked before use. Existing item/commission backing-index slot mappings are upgraded before projection.
- Keep six simultaneous recall calls, a 200-document per-context union and a 1,000-pair request bound. Legacy pages prefetch at most 20 eligible broadcasts.
- Rule coefficients remain the explicit formula in versioned code; gates/scales/half-life are external per-kind/mode settings. No fixture thresholds are presented as approved production configuration.
- Delivery uses request snapshots with no post-ranking or cached-response revalidation. Existing item detail assembly skips missing legacy-page candidates. History/claim and sample writes are independent best-effort background operations; response/page caches retain their own semantics.
- The unified result removes numeric scoring diagnostics; the existing commission compatibility DTO keeps its numeric score field.
- Commission exact-ID lookup remains the existing embedding-free ES lookup with its active/range predicates. The new remote authority hydration applies to query/recommendation candidates; exact lookup is an explicit compatibility boundary.
- Required authority failures and invalid state fail closed; partial recall is explicitly marked. No active constrained Need broadens into a generic recommendation.

## Release gates

The cutover switch remains disabled. No deployment, production migration, real
feedback publication or relationship action was performed.

Before enablement, supply the reviewed taxonomy asset and embedding version,
review representative examples for all three kinds/two modes, approve rule
configuration, verify remote commission authority/projection behavior and
external sample readers, and measure the accepted latency targets at agreed
corpus/concurrency. Unknown public provider evidence stays unknown and cannot
pass a corresponding hard filter. These are release dependencies, not reopened
Owner product questions.

## Local environment note

Validation used a separate Docker project and explicit PostgreSQL/Redis/ES/API
ports. The initial startup migration helper defaulted to another local database
because `PG_DSN` was unset even though `POSTGRES_PORT` was overridden. Its empty
105/106 additions and unused 104 change were rolled back to the original version
103, and subsequent runs used an explicit isolated `PG_DSN`. No production
connection was used. Build/log/config secrets remain in ignored local files.
