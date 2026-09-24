# Search and Recommendation MVP

The server-controlled `ENABLE_NEED_SEARCH` switch enables the three-kind,
rule-only discovery pipeline. Its default is `false`. With the switch enabled,
unified APIs and CLI capabilities appear when the existing `ENABLE_CONSOLE_V2`
gateway is enabled, and existing Feed/commission query and
recommendation routes use the new engine. Exact commission-ID lookup retains
its existing implementation. There is no per-user opt-in or new feedback type.

The [PRD](../design/need-search-mvp/prd.md), [design](../design/need-search-mvp/design.md)
and [Owner decisions](../design/need-search-mvp/questions.md) define the product.
This document describes the executable contracts and operation of this implementation.

## Entry points

All unified routes use the existing V2 Agent authentication and completed-onboarding
checks. The owner is derived from authentication; a body cannot override it.
IDs on HTTP/CLI JSON are decimal strings. Bodies reject unknown fields and are
limited to 64 KiB. Commission requests retain the existing allowlist; an omitted
kind list requests all three kinds and therefore also requires commission access.
An explicitly scoped broadcast/Agent request does not require commission access.

| Method | Path | Scope | Input |
|---|---|---|---|
| POST | `/api/v2/discovery/search` | `feed:read` | Exactly one of `query`, `need_id`, `need` |
| POST | `/api/v2/discovery/recommendations` | `feed:read` | Optional `source_kinds`, `need_ids`, explicit `filters`/`defaults` |
| GET | `/api/v2/taxonomy/search` | `feed:read` | `query`, optional `category`, `subtype`, `limit` |
| POST | `/api/v2/needs` | `context:write` | Structured Need; optional `Idempotency-Key` |
| GET | `/api/v2/needs` | `context:read` | `state`, `cursor`, `limit` (default 20, maximum 100) |
| GET | `/api/v2/needs/:id` | `context:read` | Owned Need ID |
| PUT | `/api/v2/needs/:id` | `context:write` | Replacement Need with `expected_revision` |
| POST | `/api/v2/needs/:id/state` | `context:write` | `state` and `expected_revision` |

Search defaults to 20, maximum 50 total results. Default kind order is
`broadcast, commission, agent`; explicit order is preserved by round-robin
merging. Scores from different kinds are never compared. Automatic discovery
returns at most one result, considering at most five of ten active Needs, ordered
by priority, update time, and ID. Explicit Need kinds must agree with the request.
Closed Needs are terminal; pause/resume and replacement use revision CAS.

```json
{"query":"landing page design","source_kinds":["commission"],"limit":20,
 "filters":{"budget_max_fen":50000,"currency":"CNY"}}
```

Filters apply to every requested kind. Price/delivery constraints require
commission-only scope. Category/subtype and explicitly filtered intent IDs are
hard; intent lists mean any overlap. `need.target.intents` and inferred query
intents are soft. Query prose is not parsed into guaranteed price, language,
region or exclusion constraints. Missing required evidence rejects a candidate.
Provider region never inherits owner geography. Language defaults require
`defaults.language="card"`; structured Need defaults belong inside the Need,
not alongside `need`/`need_id`.

Explicit query searches that include Agents first resolve current database
identity fields: decimal `agent_id`, case-sensitive five-letter `short_id`, then
whole-name equality against `agent_name` or `agent_name_en`. Leading/trailing
query whitespace is trimmed; short IDs and names are not case-folded. Short-ID
hits take precedence over names. Same-name Agents can produce multiple results,
bounded to 100 candidates and the normal response limit. Exact matches replace
the Agent semantic candidate pool for that request, including when hard filters
subsequently exclude every match. Other requested kinds retain their own search.
They still undergo public Card hydration, active-account, self, block and hard
filter checks, but do not require semantic score/activity thresholds. Results
carry `match.exact` (`agent_id`, `short_id`, or `name`) and `exact_match` score kind;
samples use scorer version `agent_identity_v1`.

Agent-only exact hits do not call embedding or ES. Numeric queries with no match
(including out-of-range IDs) return no Agent results rather than fuzzy matches.
Non-numeric text without an identity/name hit continues ordinary text retrieval:
a five-letter word can be prose as well as a short ID, so a wrong-case short ID
may produce semantic results but never an exact short-ID match. Names come from
current `agents` identity fields rather than the asynchronously rebuilt Card;
Agent previews also use the current public display name. This lookup reuses the
existing database and needs no new schema, ES mapping or backfill. Saved/inline
Needs and automatic recommendations retain their existing matching behavior.

Responses include `pipeline_version`, `input_origin`, `context_id`,
`effective_filters`, `constraint_mode`, `result_status`, partial/fallback reasons,
and typed `source_ref` results. Only broadcasts include `item_id`. Per-result
match metadata contains rule type/version/kind. Numeric ranking scores stay in
internal serving envelopes/samples; the existing commission compatibility DTO
retains its numeric score contract.
Internal private context snapshots and feature vectors are not returned in results.
Unified endpoints have no cursor; `has_more` is false.

No active in-scope Need falls back to frozen current Agent context, then a marked
hot/new broadcast baseline if context is empty. Agent/commission-only scope with
empty context returns `insufficient_context`. An active constrained Need with no
match is never broadened. Other empty statuses are `no_match`, `below_threshold`,
and `exhausted`. Required context/source/Redis failures return errors; optional
recall failures are explicitly partial.

## Service boundaries and storage

- `rpc/sort/discovery`: typed contracts, operation dispatch, compiler, hard filters, rule scorers and bounded orchestration.
- `rpc/sort/discovery/store.go`: owned saved/ephemeral contexts, CAS and durable Need-create idempotency.
- `rpc/sort/discovery/source.go` and `source_query.go`: existing broadcast/commission indices and public Agent index, with broadcast DB hydration and Agent/commission Redis forward projections.
- `rpc/sort/discovery/index`: shared vocabulary, slot schema and projection used by query execution and index writers.
- `rpc/sort/discovery/transport`: shared RPC JSON response codec.
- `rpc/sort/legacy/discovery_policy.go`: existing freshness, boost, injection and source-limit policies, after eligibility.
- `rpc/feed/delivery`: response/page caching and independent best-effort exposure recording.
- `pkg/agentindex`: public Card search/forward projections with version fencing and tombstones.
- `pkg/commissionindex`: catalogue search projection and independent catalogue/statistics forward components.
- `rpc/sort/discovery/index/forward.go`: bounded Redis batch reads and monotonic component writes shared by projection owners.

Successful discovery requests and Need writes retain the existing runtime/activity
observation behavior. Need/taxonomy reads and failures do not refresh activity.

`SortService.Discovery` handles contexts, ranking, validation and taxonomy.
`FeedService.Discovery` owns delivery. Their Thrift envelopes carry strictly
decoded JSON domain contracts; generated code comes from `idl/sort.thrift` and
`idl/feed.thrift`. Internal legacy-prefetch operations are not HTTP operations.

Migration 105 adds `discovery_contexts` and `processed_items.retrieval_slots`.
Saved Needs and temporary searches share the context table. Vectors are stored
separately from compiled JSON. Ephemeral contexts expire after 30 days; an hourly
maintenance job removes expired rows in batches with a five-minute run budget.

Broadcast and commission ES documents gain `retrieval_slots`; native language
and reviewed canonical aliases populate known fields. Public Agent projection
uses its own configured versioned index (default `agent_discovery_v1`) in the
existing cluster, not a separate search service. Startup verifies its mapping
and embedding dimensions. Projection reads public Card fields only. Public Card
updates and the existing maintenance rebuild path update the index; Redis tombstones exclude unavailable Cards even while ES catches up; current
account and block checks remain database-backed. Automatic people discovery excludes existing
friends and nonempty PM conversations. Query people search retains those contacts.

Commission provider-region/language evidence is currently unavailable at its
source boundary and stays unknown. Region constraints therefore reject those
rows; no location is inferred from the owner. Broadcast/Agent provider region
also remains unknown without a separately approved public source field.

### Search index and forward index

For Agent and Commission, ES stores only fields used for retrieval/filtering,
plus IDs and version metadata required to join projections. Agent ES keeps
public search text/name, active state, slots and embedding; activity/edit times
are not in ES. Commission ES keeps weighted searchable text, active state,
seller ID, slots, price/currency/promised duration and embedding. Fulfillment,
ratings, counts, statistics revision and update time are not in ES. Price and
embedding intentionally exist in both stores because they serve retrieval and
scoring. Broadcast storage and scoring reads are unchanged.

ES responses return only candidate IDs, source revisions, `_index` and channel
scores. Sort then batches Redis forward reads for the deduplicated candidates:

| Key | Contents and revision |
|---|---|
| `discovery:forward:agent:<concrete-index>:<id>:card` | Public Card projection, vector, slots, activity time; Card rebuild fence |
| `discovery:forward:commission:<concrete-index>:<id>:catalogue` | Catalogue projection, vector, slots, budget/duration evidence, update time; catalogue revision |
| `discovery:forward:commission:<concrete-index>:<id>:statistics` | Completion/rating/count/delivery features; independent statistics revision |

These are reconstructible projections without TTL, not cache-aside entries.
Namespaces use concrete ES generations so staged backfills cannot change the
currently served generation. Missing components and revision mismatches skip the
candidate and increment `discovery_rejected_total` with `forward_missing` or
`forward_version`; Redis errors and corrupt components fail the request. No
online DB/RPC fallback fills missing ranking features with defaults. Existing
account/block/contact checks and current public display names still come from
bounded DB queries. Exact Agent identity lookup retains its DB-only path because
it does not rank by activity or semantic features.

Existing Card writers and commission consumers/backfills write the forward
projection before the ES document. The stores are eventually consistent; there
is no cross-store transaction. Version guards reject older writes, including
older tombstones. Commission statistics events update only the statistics
component, without catalogue RPC, embedding or ES writes. Failed writes retain
the existing consumer retry behavior. New online commission ranking no longer
calls catalogue/statistics RPCs per request. Its availability and price snapshot
follows projection updates; irreversible transaction validation remains with the
owning service. Samples retain the concrete source index and feature revisions.
Legacy commission search/exact-ID adapters also read statistics from Redis.
Before deploying these readers, deploy the projection writers and backfill the
forward components for their served ES generation. This prerequisite applies
even when `ENABLE_NEED_SEARCH=false`; changing the route switch does not restore
the old ES-feature storage contract.

Recall uses lexical/dense/structured channels and existing hot/new/new-UGC Redis
lists where enabled. No Swing lane or learned scorer is called by this engine.
Fan-out is bounded to six concurrent channels, 200 merged documents per context,
100 commission/Agent candidates per kind, and at most five contexts. Forward reads and current account/relationship checks use bounded batches. Rule features, gates, configuration
hash, request time, contributions and final policy score are frozen in samples.

## Delivery, pagination and samples

Query search has within-response dedup only. It writes broadcast validation state
to `impr:search:agent:<id>:items`, independently of automatic history. Automatic
broadcast discovery retains existing item/group/URL impression keys, rolling
Bloom history and injection claims. Other automatic kinds use typed membership
in `impr:discovery:agent:<id>:items`.

Requests with an `Idempotency-Key` cache the assembled response for 24 hours,
scoped by owner plus a hashed key. Matching retries return that response and
impression without querying Sort or checking mutable Need/source state again;
changed payloads return 409. Requests without a key do not create a response
cache. Cache failures remain errors when the caller requested idempotency.

Needs and rules use the execution snapshot. Sort hydrates broadcast source facts and Agent/commission forward projections
once per context before filtering and scoring; it performs no extra
post-ranking hydration or `Revalidate` RPC. Later Need/source changes affect new
requests. Assembled response caches may remain stale for their TTL.

History/claim updates and delivered sample publication run independently in the
background, each with a two-second timeout detached from request cancellation.
They do not share a transaction with response/page caches and cannot fail the
response. Failures emit logs and `discovery_recording_failures_total{stage}`.
No durable retry/outbox is added: transient history gaps may permit repeated
recommendations, and missing samples may cause feedback joins to miss. Cached
response retries do not publish another exposure or repair missing samples.

Legacy Feed keeps `refresh`, `load_more`, `has_more`, the existing item DTO and
an additive `discovery` metadata object. Its separate
`discovery:feed:<owner>:page` cache retains at most 20 frozen candidates for 30
minutes. Each page returns at most one item, with one shared impression ID and
absolute sample position. Page assembly calls the existing item detail lookup;
missing/non-completed items are skipped and the next cached candidate is tried.
Need/context changes do not invalidate a frozen page. Cursor advancement is
saved before best-effort recording and is independent of its success. Prefetched
or skipped candidates are not exposures. Old-generation feed caches are not
read by the new adapter.

Migration 106 extends the same `replay_logs` table and Redis stream:

| Rows | Pipeline | Mode | Schema | Identity |
|---|---|---|---|---|
| Existing/legacy | `legacy_feed_v1` | `feed` | 1 | Broadcast `item_id` |
| New | `need_search_v1` | `search` or `recommendation` | 2 | `source_kind`, `source_id`; `item_id` only for broadcasts |

New rows also have context/Need/revision fields. `agent_features.search_context`
contains frozen contexts and provenance; `item_features.search` contains the
candidate, rule evidence and policy result. `item_score` is the final policy
score. The existing unique `(impression_id, position)` key makes consumption
idempotent. No reject/empty result creates a row or a negative label. Internal
Feed readers restrict to broadcast Feed/recommendation samples; legacy
feature-dependent rescue reads restrict to the legacy generation.

CLI 0.0.53 adds `search`, `recommend`, `taxonomy search`, and Need lifecycle
commands. The ef-broadcast Skill is 0.14.15. Broadcast feedback retains existing
meaning; `feed event record --impression-id` selects the exact cached impression
when the same item appeared in multiple searches. Nonbroadcast IDs never enter
broadcast feedback. People results do not trigger messages or friend requests.

## Configuration and rollout gates

`ENABLE_NEED_SEARCH=true` requires `ENABLE_COMMISSION_INDEX=true`,
`ENABLE_COMMISSION_DISCOVERY_API=true`, and `ENABLE_REPLAY_LOG=true`. Preserve the
existing commission allowlist. Every participating process must share the same
cutover setting, taxonomy version and embedding configuration.

`DISCOVERY_TAXONOMY_PATH` defaults to `configs/discovery/taxonomy.json`.
The reviewed JSON asset has `version`, `embedding_version`, and `categories`,
`subtypes`, `intents` arrays. Nodes contain `id`, `name`, optional parent
`category`/`subtype`, `aliases` and `embedding`. Embedding version must equal the
configured model, and nonempty vectors must match its dimension. Canonical-only
rows may omit vectors. Lookup is bounded in-memory cosine/exact-alias matching.
No taxonomy cache or rebuild workflow is included in this implementation.

`DISCOVERY_RULES_PATH` defaults to `configs/discovery/rules.json`. Its root has
`broadcast`, `commission`, `agent`; each has `search` and `recommendation` rules.
Each rule requires `version`, `bm25_scale`, `cosine_floor`, `min_relevance`,
`threshold`, and `half_life_ms`. The MVP formulas in `rpc/sort/discovery/score.go` are
versioned code; gates/scales/half-life are independent per kind/mode. Initial
formula coefficients must also be reviewed against supplied examples before
cutover. There are deliberately no invented production taxonomy/threshold assets.
`AGENT_DISCOVERY_INDEX` selects the public Agent index.

Rollout order:

1. Apply 105/106 to the intended database. Set `PG_DSN` explicitly when using nondefault local ports.
2. Deploy typed-aware replay consumers and verify all internal/external readers; keep these readers after a routing rollback.
3. Supply reviewed taxonomy and three-kind/two-mode rule examples/configuration. Keep API traffic on the old path during preparation.
4. Use new concrete Agent/commission ES generations when removing old mappings. Existing ES mappings cannot delete fields in place; full document rewrites remove obsolete `_source` fields. Align writer/reader generations, backfill Redis as well as ES, and retain both old generations for rollback. Commission writers target `COMMISSION_INDEX_NAME`; alias promotion follows a successful staged backfill.
5. Populate both forward and search projections with `scripts/discovery_backfill --kind broadcast|agent` and the existing `scripts/commission_backfill`, using the approved taxonomy in those maintenance processes. These are index maintenance tools, not a new offline ranking pipeline.
6. Verify strict-filter coverage, source permissions, embedding compatibility, rule examples, and measured load targets. Enable the cutover switch consistently and use existing deployment/PR procedures.

Rollback routes with `ENABLE_NEED_SEARCH=false`; retain additive schema and typed
readers. Migration 106 refuses downgrade while nonbroadcast rows remain. Do not
coerce typed IDs into `item_id` or discard samples to make a downgrade succeed.

Metrics expose execution latency/status, hard-filter/threshold/seen rejects,
optional channel failures, explicit fallback reasons and taxonomy misses. Their
labels contain bounded codes, never owner IDs or query text. Accepted latency
targets require a separately agreed corpus/concurrency test; unit/integration
results are not production latency evidence. In particular, Agent-context fallback
can make up to five embedding requests; validate its cold/warm latency before
cutover and add versioned memoization if the agreed budget requires it.
