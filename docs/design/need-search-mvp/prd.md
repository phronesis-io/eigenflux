# Search and Recommendation MVP — PRD

Status: Revision 3, incorporating all Owner answers and the clarification that all three source kinds launch in the MVP. Implementation and validation are tracked in [implement.md](implement.md); production cutover remains gated.
Date: 2026-09-22. Repository baseline: `origin/main` at `9a532e79`.
Companions: [Technical design](design.md), [Owner decisions and remaining dependencies](questions.md).

## 1. Objective and scope

Replace the current discovery implementation with one forward retrieval service supporting two user-facing modes:

1. **Automatic search**, the successor to daily recommendation: use the authenticated Agent's information and active Needs to find the next useful result, retaining existing cross-request deduplication.
2. **Query search**, traditional explicit search: accept a query directly, return a ranked list, and deduplicate only inside that request.

Both modes support **broadcasts, commissions/services, and Agents/people** from the first release. Structured Needs remain a precise, persistent way to express intent; they are not a prerequisite for either mode. One deterministic compiler/planner/filter/ranking path serves all inputs, with explicit input provenance.

The reference is [the architecture proposal](https://pcnlty6lw65j.feishu.cn/docx/KRPPdM4xWoDgcrxsl2gcMRfVn8g), revision 83, especially its section 2.1 MVP diagram and sections 3.1, 3.4, and 3.5. Owner answers D03/D04 supersede the first draft's additive-only rollout and mandatory structured-Need entry. Agent information is an authorized query source in automatic mode; it must not silently rewrite an explicit query or structured Need.

| Area | MVP boundary |
|---|---|
| Top-level interfaces | Agent-based automatic search, raw-query search, captured Need selection, taxonomy lookup, existing-route adapters, CLI |
| 3.1 Compiler | Compile Agent-authored Need, raw query, or bounded Agent context into an executable search context; rule validation, taxonomy lookup, existing embeddings |
| 3.2 State | Explicit active/paused/completed/expired Needs; no dialogue or authority engine |
| 3.3 Planner | Deterministic per-kind templates, bounded fan-out, shared hard-filter semantics |
| 3.4 Retrieval | Forward lexical/dense/structured retrieval; reuse DB/ES/Redis and current recall producers |
| 3.5 Hard filter | Pushdown, source hydration and shared deterministic state/permission evaluator |
| 3.6 Rank | All three kinds use rules this release; future model adoption, size, versions, and rollback are independent by kind |
| 3.7–3.9 | Migrate applicable existing policies, delivery primitives, feedback API, and CLI events; only necessary adapters |
| Samples | Same replay table/stream; delivered-only records; explicit generation, input, mode, and per-kind scorer metadata |

Excluded: reverse/percolator matching, content-event fan-out, offline enrichment architecture or taxonomy clustering, training/calibration/model serving, new feedback classes, automated Need completion, delivery holding queues, fairness redesign, and new exploration policies. Index projection contracts and necessary migration readiness are specified; the offline producers themselves are not designed here.

## 2. Confirmed product behavior

### 2.1 Three kinds with independent evolution

Use the shared NeedInput `need_type` values `broadcast`, `commission`, and `agent`. An explicit query can select one or multiple kinds. The unified API defaults to all three; existing broadcast/commission routes stay restricted to their current kind.

Do not compare uncalibrated rule scores between kinds. Query results are ordered within each kind and combined by a deterministic quota/order policy. Automatic search orders qualifying results by Need priority, with stable ties, and deduplicates across Needs. Broadcast may adopt a learned model in a later release while commission and Agent scoring remain rules. No requirement couples model architecture, parameter count, release date, or version across kinds. The MVP adds no model training or serving work.

### 2.2 Output size

Query search defaults to 20 results per page and accepts at most 50 per page. Opaque cursors continue a frozen, owner-scoped ranking with at most 200 candidates for 24 hours. Automatic search defaults to 20 results and accepts a limit up to 100 across kinds; it returns fewer when fewer candidates qualify and never broadens constraints to fill the limit.

Existing route authentication, JSON envelope, typed identity, notification behavior, and feedback fields remain compatible, while their discovery execution switches to the new engine. Legacy Feed pagination is retained through a compatibility cache of new-engine candidates with frozen contexts, absolute positions, and existing item-detail assembly on each page. Its requested limit is honored. Unified search uses explicit continuation cursors. Exact commission-ID lookup remains exact lookup, rather than being converted into semantic search.

Missing context or no eligible candidates is a successful empty discovery result.
Each kind may contribute zero items; merge retains available results from other
kinds. All three may be empty without skipping Feed's remaining response fields.

### 2.3 Direct replacement and basic fallback

The new engine becomes the implementation behind existing recommendation/search entry points at cutover; users do not need to opt in or first create a Need. New unified routes expose multi-kind results; old typed routes never receive a different kind disguised as their existing item type.

The basic fallback contract is:

- If eligible active Needs exist, execute them with all their constraints. No match means no match; do not broaden to an unrelated profile or hot feed.
- If no active Need exists in the route's requested kinds, use bounded current Agent intent/context fields, then owner Card demand/seeking/focus or interests. Treat this as an ephemeral Agent-context search, never an invented user-approved Need.
- If Agent context is empty, use a bounded baseline from existing fresh/hot broadcast lists; for a service/people-only route, return `insufficient_context` instead of inventing a preference.
- If an optional semantic/recall channel is unavailable, continue with available channels under the same hard constraints and report partial execution. Permission checks, authoritative state reads, and explicit filters never fail open.

These are concrete implementation defaults for the owner's “basic fallback” requirement. Every response/sample records the actual path and fallback reason. Query mode never falls back to unrelated Agent context or a generic list.

## 3. User journeys

### 3.1 Daily automatic search

The existing host poll calls its usual recommendation/feed entry. The server derives identity from auth, selects up to five eligible captured Needs, or uses the defined Agent-context/baseline fallback. It retrieves all enabled kinds relevant to that request, applies rules and existing policies, and delivers up to the requested limit of eligible results. No new resident process or scheduling service is introduced. Private owner context may be read to serve that owner; it is not exposed to recommended providers or public Agent search.

### 3.2 Query search

The user or Agent submits `query` plus optional kinds and explicit filters. A category is not mandatory for a traditional query. If supplied, category/subtype are hard boundaries and unknown content evidence rejects a candidate. Taxonomy suggestions from text are soft retrieval signals unless explicitly selected as filters. Numerical conditions inside free text are not claimed to be enforced as structured filters; clients use the filter fields for guaranteed constraints, and the response returns the effective filters.

Results can repeat across requests. Within a response, collapse duplicate typed sources and applicable broadcast groups. Search does not read or mutate automatic-search history. Existing feedback validation is adapted so explicit-search broadcasts remain reportable without suppressing later recommendation.

### 3.3 Save and maintain a precise Need

The Agent captures a `need_input.v2` interpretation of a confirmed Intent using
`need input create`. Search consumes the original input; automatic discovery
selects eligible rows from `current_need_inputs`. Intent changes require a new
capture. There is no parallel Need CRUD or normalization flow in Sort.

Typed constraints remain hard and source JSON remains unchanged. Open mandatory
requirements without verification and unresolved legacy code restrictions yield
no candidates for that Need, with diagnostics; other Needs can still contribute.
Preferences never become hard restrictions. Capture remains available without
embedding/ES/Redis, and cached serving responses retain their original snapshot.

### 3.4 Find a person

Need type or query kind `agent` searches public Agent capability/Card content. The server checks discoverability, self/block/relationship restrictions, and activity according to existing domain rules, then returns a public Card reference. Recommending a person does not send a PM, request friendship, or mutate a relationship. Private owner geography is not silently indexed as provider geography.

### 3.5 Feedback and attribution

Broadcast feedback keeps existing event meanings and queue behavior. Preserve exact impression context where known. Commission actions and Agent relationship/PM actions remain in their current domains; neither is coerced into broadcast `item_id` events. Feedback does not auto-edit/complete a Need. Need-seeded Swing remains disabled because `surface` is not confirmed adoption.

## 4. Functional acceptance requirements

| ID | Requirement | Evidence |
|---|---|---|
| F01 | Owned operations and context | Another Agent cannot read/use a Need or search using another owner's private context |
| F02 | Two first-class entry modes | Raw query works without taxonomy/Need authoring; existing daily feed works without saved Needs |
| F03 | All three source kinds | Broadcast, commission, and public Agent results are hydrated and represented by typed IDs |
| F04 | Input provenance | Saved Need, inline Need, query, Agent context, and baseline are distinguishable in snapshots |
| F05 | Explicit-query independence | Unrelated Card edits do not rewrite a query/Need's terms, target, or constraints |
| F06 | Shared hard constraints | Every channel and fallback passes the same applicable evaluator; injection cannot bypass it |
| F07 | Missing evidence | Explicit price/currency/region/language/category constraints do not accept unknown values |
| F08 | Deterministic rule MVP | Same frozen context, candidates, clock, and configuration produce the same output; no learned ranker call |
| F09 | Independent scorer versions | Each source kind carries its own scorer type/version; no mixed-kind score comparison |
| F10 | Meaningful empty/error distinction | No match, baseline fallback, partial retrieval, and required-backend failure are distinguishable |
| F11 | Identity and dedup | Typed source IDs do not collide; query dedups within request only; automatic mode retains history |
| F12 | Request snapshot and assembly | Validate Need at execution start and source facts during hydration; reuse assembled responses without revalidation; subsequent requests observe changes |
| F13 | Sample compatibility | Same replay table/stream, explicit old/new markers, delivered-only semantics, compatible old decoding |
| F14 | Exact attribution | Selected Need/context and actual scorer are frozen with the delivered row; no reconstruction from mutable current data |
| F15 | Replacement compatibility | Existing routes invoke the new engine with their kind/auth/envelope; no stale legacy cached pages cross cutover |
| F16 | Bounded fallback | Active constrained Needs are not replaced by unrelated content; query never switches to baseline |

## 5. Rule quality and performance

Rule weights and thresholds must be **adjusted against reviewed examples**, not treated as approved production constants. Fixtures must cover all three kinds, both modes, missing slots, conflicting constraints, Agent-context searches, and baseline delivery. Record separate configurations by kind and mode; retain a relevance gate before boosts/injection. All scores are heuristics, not `P(useful)` or calibrated utility. Baseline freshness/quality ordering has its own score kind and does not claim relevance to a nonexistent query.

Accepted provisional targets: five-context automatic search P95 ≤ 500 ms, already compiled single-context query search P95 ≤ 300 ms, and compilation/inline search P95 ≤ 2 s including embeddings. Multi-kind execution must be bounded; measure cold/warm performance at a specified concurrency and corpus size before declaring an SLO achieved. Those workload numbers and the reviewed-example fixture owner remain launch inputs.

Track errors separately from empty results; candidate/filter yields by kind/channel; fallback reasons; scorer distributions/versions; sample marker coverage; and exact feedback attribution. Missing feedback is unknown. Temporary contexts retain 30 days; replay retains the existing configured policy. No full rejected-candidate dataset is promised.

## 6. Reuse and required changes

Reuse existing PostgreSQL, item and commission ES indices, Redis recall lists, policy implementations, replay stream/table, feedback tables, and CLI event queue. Reuse `need_inputs` and `current_need_inputs`; keep `discovery_contexts` only for execution snapshots, plus revisioned caches and normalized slot fields. Avoid making a new microservice deployment.

People search needs a new rebuildable public Agent projection because no equivalent Need-based Agent index was verified in the repository. Use the existing ES cluster with a separate small Agent index and the existing Card/domain source; never mix Agent documents into item indices. This is the minimum additional kind-specific index, not a new infrastructure platform. Its update/source contract is part of online design; a general offline feature/index platform is not.

Reusing `replay_logs` for three types requires typed identity metadata and nullable broadcast `item_id` for nonbroadcast rows, with consumer/read compatibility checked before cutover. Preserve `(impression_id,position)` uniqueness and all historical values. Add pipeline metadata independently of each kind's scorer version so later broadcast model upgrades do not relabel all other traffic.

## 7. Implementation slices and launch gates

| Slice | Outcome |
|---|---|
| A. Input/contracts | Query and automatic interfaces, context compiler, accepted decision record, fallback matrix |
| B. Structured Needs | Taxonomy asset/lookup, current captured-Need readers, bounded execution snapshots and exact provenance |
| C. Typed retrieval | Broadcast/commission adapters; public Agent projection and adapter; uniform hard checks |
| D. Rules/policies | Three independently configured rule scorers, reviewed examples, migrated dedup/policy behavior |
| E. Serving/samples | Source hydration, existing-route replacement, exact CLI attribution, typed replay consumers/readers |
| F. Cutover | All three kinds ready, shadow comparison without delivery side effects, server-controlled switch and rollback |

Implementations can be validated in slices, but the confirmed MVP launch scope is all three kinds. No user opt-in is required at final cutover. This documentation revision does not implement or deploy the service.

## 8. Representative end-to-end scenarios

1. An existing Agent with no saved Needs receives automatic search from current Agent context; an empty context yields an explicitly marked baseline broadcast or `insufficient_context` on nonbroadcast routes.
2. A direct query works without category, outcome, or a pre-created Need; optional explicit filters remain hard and effective filters are returned.
3. All three kinds appear in unified query results under deterministic quotas; automatic search honors the requested limit without padding sparse results.
4. Existing Feed returns broadcasts; existing commission recommendation returns commissions; both use the new engine and their existing auth/envelopes.
5. An active Need with no eligible candidates yields empty results, not an unrelated fallback.
6. Unknown provider region is rejected when explicitly required; owner-private geography is never used as public provider evidence.
7. Price/currency and absolute-deadline-versus-duration comparisons remain correct, including known zero price.
8. An injected UGC candidate still passes hard constraints and the relevance gate; baseline has explicitly different, unpersonalized eligibility semantics.
9. Each new request observes current Need/source state during context loading and hydration. In-flight executions and assembled response retries use their snapshot; recording failures do not fail delivery.
10. The same query may return the same result tomorrow; daily automatic search retains deduplication across calls.
11. Returned broadcast feedback joins the original impression/context; commission/Agent IDs never become broadcast IDs.
12. Old and new producers share the replay stream after consumer upgrade; replay retains delivered-only granularity and exact kind/scorer markers.
13. Installing a legacy LR bundle does not affect any of the three MVP paths. A future broadcast model can change independently of commission/Agent rules.
14. Retried requests preserve impression identity without returning newly ineligible content; required authority failure remains an error.

## 9. Decision status

Owner answers D01–D14 are incorporated in [questions.md](questions.md), preserving the original answers. D01 was clarified to include people. The remaining items are implementation/launch dependencies—reviewed examples and final parameters, taxonomy artifact/owner, public provider evidence and Agent indexing readiness, external sample-reader compatibility, and workload measurements—not a request to repeat the product questionnaire.
