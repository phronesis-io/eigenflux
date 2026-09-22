# Owner Decision Record — Search and Recommendation MVP

Status: Revision 2. All Owner answers below are preserved verbatim and incorporated in the [PRD](prd.md) and [technical design](design.md). The subsequent D01 clarification explicitly selects broadcasts, services, and people for the first release.

The questions and “Proposed default” paragraphs are retained as the original discussion record, not as current specifications. The **Resolution** under each answer describes the current decision. Owner answers take precedence over the original defaults. Concrete API/storage/fallback details remain technical design choices unless explicitly confirmed by an answer.

## D01 Content types

**Question:** Should the first release include broadcasts and commissions, or also Agent/person discovery?

**Proposed default:** `find_info → broadcast`, `find_service → commission`; `find_people` is explicitly unsupported initially. No secondary-kind cascade or cross-kind score comparison.

**Alternatives:** Broadcast-only first; or all three kinds. Agent discovery requires an approved searchable public Card projection, provider-region provenance/consent, relationship exclusions, and typed result/sample support. Reusing Console Home recommendations does not by itself provide Need-based search.

**Why this needs a decision:** The source's section 3.6 prose says one primary kind per Need, but its matrix gives `find_service` two primaries. The MVP diagram does not settle the launch types. Existing sample/event contracts are primarily broadcast-based.

Owner answer: 是的, 但是不需要统一模型的发展进度,规模/version. broadcast可以先上模型,其他的用规则

**Resolution:** Confirmed by follow-up: launch all three kinds (broadcast, commission, agent). All use rules in this MVP; later model adoption, architecture/size, release timing, and versions are independent per kind. No requirement to upgrade all three together. The original proposal to defer people is superseded.

## D02 Search and recommendation output

**Question:** Is the intended contract interactive top-K search plus zero-or-one recommendation, or should recommendation keep today's batch size?

**Proposed default:** Search default 20, maximum 50; recommendation at most one; neither new endpoint has pagination in the MVP. Existing Feed pagination remains unchanged.

**Consequence:** Top-1 preserves the source's standing-query product. Batch recommendation reuses more of existing Feed delivery but needs explicit multi-Need merge and allocation semantics.

Owner answer: default answer is fine

**Resolution:** Query search defaults to 20 results, at most 50 on the unified API; automatic search returns zero or one across kinds. Unified APIs have no pagination. Existing Feed pagination is retained through a compatibility adapter with frozen context snapshots and item-detail assembly, while every automatic response has at most one discovery item.

## D03 Entry point and no-Need behavior

**Question:** Should the new capability be additive/opt-in or replace current Feed and commission recommendation endpoints immediately?

**Proposed default:** Additive APIs and CLI commands with explicit opt-in. No active Needs returns `no_active_needs`; no profile-query fallback. Keep Card `seeking/demands/current_focus` as existing display/context fields without new automatic consumers.

**Consequence:** Immediate replacement needs a client migration plan and an explicit decision for existing users without Needs. It must not silently change old batch response contracts.

Owner answer: 可以,直接replace,但是要有基本的fallback

**Resolution:** Directly replace the implementation behind current discovery entry points; no client opt-in requirement. Preserve their authentication, kind restriction, DTOs, and pagination/exact-lookup adapters. Basic fallback is concretized as no active in-scope Need → current Agent context → a marked fresh/hot broadcast baseline if context is empty. An active constrained no-match is not broadened. Required authority/read failure is an error. The detailed fallback order is a documented implementation choice within this requirement.

## D04 Raw query and old Skill compatibility

**Question:** Must the server accept free-text-only search/Need submissions, or can the Agent always submit the structured schema?

**Proposed default:** Agent/Skill structures input; backend only validates/maps/embeds. CLI commands accept JSON or an existing Need. No server LLM fallback this release.

**Alternative:** Enable a bounded `compiled_by=server_fallback` path for older clients. This adds an LLM dependency, evidence validation for extracted constraints, latency/error contracts, and a retirement condition. A simple lexical-only `query` path would need a separately defined incomplete-Need contract; it cannot pretend all Need constraints were understood.

Owner answer: 支持两种搜索, 一种是直接根据agent的信息搜(即原来的推荐), 另一种是输入query的搜索,即比较传统的搜索模式.

**Resolution:** Two first-class modes: automatic search from current Agent information, and traditional explicit-query search. Saved/inline structured Needs remain optional precise inputs. Raw query does not require category/outcome/Need authoring. Use rule/taxonomy/embedding normalization without introducing a generative server compiler. Inferred taxonomy remains soft; structured filter fields carry enforceable constraints.

## D05 Profile defaults and hard constraints

**Question:** Should missing language/provider-region constraints inherit Card values automatically, require explicit opt-in, or remain unconstrained?

**Proposed default:** No provider-region inheritance; language inheritance only when explicitly requested. Owner exclusion is mandatory. Record origins for every effective constraint; an explicit hard constraint with missing content evidence rejects the candidate.

**Why depart from the source default:** Owner geography is not provider-location intent, and current geography is owner-private. Automatic inheritance can silently reduce recall or violate the user's actual request. Full authority-based exceptions remain out of scope.

Owner answer: yes

**Resolution:** Accepted: no owner-geography-to-provider-location inheritance; Card language inheritance only by explicit opt-in; mandatory self-exclusion. Preserve effective origins. Explicit hard constraints reject missing evidence.

## D06 Constraint semantics and legacy slot coverage

**Question:** Are category/subtype strict eligibility boundaries, and may old documents without canonical slots participate? What is the authoritative source for commission provider region/languages and normalized slots?

**Proposed default:** Category/subtype are strict; intents are relevance signals. Admit only documents with sufficient evidence for every hard condition. Retain existing ES indices; add canonical slots with one active taxonomy version. Missing fields do not auto-match.

**Related definitions to confirm:** Budget compares known minor units only in the same currency; deadline is absolute and commission delivery is a duration; broadcast deadline expires the Need rather than defining document recency. Provider geography and content geography are separate.

**Consequence:** Strict slot matching needs projection coverage before enabling a type. A transitional lexical/dense lane for old documents can increase coverage, but must disclose that category is then a soft preference and must not bypass explicit language/price/region constraints. That is a product-contract change, not an internal optimization.

Owner answer: default answer is fine

**Resolution:** Accepted: explicit category/subtype constraints remain hard; Need target intents are relevance signals; unknown required evidence rejects. Query/Agent contexts that omit category are legitimately unconstrained by category, not a legacy-data bypass. Preserve money/currency/duration/deadline distinctions. Authoritative provider fields/projection ownership remain implementation readiness dependencies.

## D07 Taxonomy bootstrap and ownership

**Question:** Who provides and approves the initial category/subtype/intent vocabulary and term embeddings? Should miss persistence be a separate table in this release?

**Proposed default:** One manually reviewed versioned artifact shared by Need and content adapters; preserve original phrases in Needs and record miss counters/logs. No monthly rebuild/clustering implementation or miss-review workflow. Unmapped intent phrases are allowed.

**Consequence:** Without a supplied vocabulary and compatible content projection, exact slot retrieval cannot be claimed ready. If persistent aggregation of misses is required now, add a small `taxonomy_misses` table but keep clustering and taxonomy production out of scope.

Owner answer: Proposed default is good

**Resolution:** Accepted: manually reviewed immutable taxonomy asset shared by adapters, preserve original phrases and miss counters/logs, no monthly builder or miss-review subsystem/table. Artifact supply and operational owner still need to be assigned before launch; the answer does not identify a person.

## D08 Search history and deduplication

**Question:** Should an item returned in explicit search suppress later recommendation?

**Proposed default:** No. Search is repeatable and uses a separate feedback-validation impression set; recommendation keeps existing Bloom/impression/claim behavior. Feedback validation accepts either eligible source while preserving exact impression attribution.

**Alternative:** Reuse the same delivered-history writes for both, which is cheaper but makes search results affect existing recommendation/Swing exclusion even if the user never surfaces them. Need-level rather than agent-level deduplication is another distinct change and is not assumed.

Owner answer: query-based search 在同一请求内去重,不需要跨请求去重; 日常自动的搜索(即现推荐)保持现有去重逻辑

**Resolution:** Explicit query search deduplicates only within its response and does not read/write automatic history. Daily automatic search keeps cross-request history semantics; typed adapters extend them to service/Agent IDs. Separate explicit-search broadcast validation state prevents feedback support from accidentally suppressing recommendation.

## D09 Rule score and threshold policy

**Question:** Can the initial rules use the proposed feature weights and per-kind thresholds, and who reviews the representative relevance examples?

**Proposed default:** Relevance dominates freshness/quality; require a separate relevance gate before boosts/injection. Start with the explicit formula and thresholds in design section 3.6, then adjust against reviewed examples. No probabilities, old LR calls, learned reranking, or online calibration.

**Consequence:** These numbers are initial operating parameters. Relevance quality and result coverage cannot be guaranteed from architecture alone. Confirm whether search should use a lower threshold than unsolicited recommendation; the draft uses the same gate for predictability.

Owner answer: adjust against reviewed examples

**Resolution:** Weights and thresholds are to be adjusted against reviewed examples. Revision-1 numbers are fixture starting points, not approved production settings. Review all three kinds and both modes, record accepted config/version, and keep relevance gating before boosts. The MVP stays rule-only.

## D10 Need fan-out and fairness

**Question:** Is a limit of 10 active Needs, selecting up to five by priority per request, acceptable for the MVP?

**Proposed default:** Yes; deterministic priority order and stable ties. Equal-priority different-kind candidates are not compared by uncalibrated scores. No dynamic priority adjustment or weighted-round-robin policy this release.

**Consequence:** Lower-priority Needs can starve. Including fairness now is a small but real addition to the deferred section 3.7 design. Alternatively evaluate all 10 Needs with a larger measured latency/cost budget.

Owner answer: YES

**Resolution:** Accepted: at most ten active saved Needs and five selected per automatic request, deterministic priority order, no new weighted fairness module. Lower-priority starvation remains a documented tradeoff.

## D11 Existing sample table and typed sources

**Question:** Approve explicit pipeline/mode/schema markers in `replay_logs`, and, if commissions launch, nullable broadcast `item_id` plus typed `source_kind/source_id`?

**Proposed default:** Reuse the same table and stream. Old rows/events are `legacy_feed_v1`; new rows are `need_rules_v1`. Keep the existing impression/position uniqueness key. Commission IDs never enter broadcast `item_id` or broadcast feedback APIs.

**Required compatibility work:** Upgrade replay consumers before V2 producers, audit internal readers, and require external legacy model exports to filter by pipeline. Typed-aware readers stay deployed after routing rollback. Existing feedback tables remain semantically unchanged; exact Need attribution comes from the original replay row.

**Alternative:** Launch broadcasts only until typed-table consumers are ready. Creating a second sample table is not the proposed approach because the requested direction is reuse.

Owner answer: Reuse the same table and stream

**Resolution:** Confirmed same replay table and stream. Technical implementation adds explicit generation/mode/schema and typed identity metadata, with nullable broadcast item_id for service/Agent rows and consumer-first rollout. Preserve existing impression/position uniqueness and historical semantics; per-kind scorer versions are independent. Exact DDL/read compatibility is implementation work, not an assertion that old readers already support it.

## D12 Sample granularity

**Question:** Does “continue using the previous sample table” mean delivered samples only, or must this release also persist all rejected/below-threshold candidates and empty decisions?

**Proposed default:** Preserve delivered-only rows and add Need/rule snapshots; rejects and abstentions have logs/metrics. No negative label is inferred from silence.

**Consequence:** Delivered-only data is insufficient for full counterfactual/rejected-candidate model evaluation later. Full recording needs a distinct candidate/decision identity and idempotency contract; fake delivered rows or synthetic item IDs would corrupt current consumers. This choice should be made explicitly before implementation if next-phase training requires those records from day one.

Owner answer: Proposed default is good

**Resolution:** Accepted delivered-only granularity. Add frozen context/Need/scorer evidence to actual returned rows; rejects and empty decisions use logs/metrics. No synthetic negative rows or inferred negative feedback. Full rejected-candidate training data remains out of scope.

## D13 Non-broadcast feedback and Swing

**Question:** Is it acceptable to preserve broadcast CLI feedback unchanged, keep commission actions in their existing domain, and leave Need-seeded Swing disabled?

**Proposed default:** Yes. Add only exact impression-context support to the existing CLI ledger; no new event classes or automatic feedback-to-Need mutations.

**Reason:** Current `surface` is not confirmed `adopt`. The source's `need:<id>:adopted` seed cannot be populated honestly by renaming existing surface history. Unifying commission/people action labels or adding adoption semantics would expand section 3.9 and should be separately scoped.

Owner answer: Yes

**Resolution:** Accepted: existing broadcast CLI event meanings/queue, nonbroadcast actions remain in their domains, exact impression-context adapter only. Need-seeded Swing remains disabled; surface is not renamed adopt. People discovery does not initiate PM/friend requests.

## D14 Performance and retention targets

**Question:** What initial concurrency/corpus size should define the SLO, and how long should temporary search Needs and new sample snapshots remain available?

**Proposed default:** Validate five-Need recommendation P95 ≤ 500 ms, compiled single-Need search P95 ≤ 300 ms, and compile/inline search P95 ≤ 2 s at agreed realistic load. Temporary Needs retain 30 days; replay keeps its existing configured retention until separately agreed.

**Consequence:** These are proposed measurement targets, not capacity promises. Temporary context must outlive the existing eight-day CLI ledger window; private Need snapshots increase the sensitivity and size of the existing replay table and must respect current access/deletion rules.

Owner answer: Proposed default is good

**Resolution:** Accepted provisional targets and retention: five-context automatic P95 500 ms, compiled single-context query 300 ms, compile/inline 2 s; ephemeral context retention 30 days and existing replay retention. Concurrency/corpus-size measurements and evidence of meeting targets remain launch tasks.

## Remaining implementation and launch dependencies

These items do not reopen answered product decisions or require another full questionnaire.

| Dependency | Required result before enablement |
|---|---|
| Taxonomy bootstrap | Reviewed asset/embeddings, assigned maintenance owner, matching projection version |
| Relevance review | Representative examples for three kinds/two modes and approved per-kind/mode rules/thresholds |
| Provider evidence | Authoritative public region/language and normalized-slot contracts, especially the commission source boundary |
| People indexing | Public-only Agent projection ready; update/tombstone freshness and bounded authoritative relation checks |
| Sample readers | Typed/generation-aware consumer rollout and internal/external reader compatibility verified |
| Workload sizing | Agreed test concurrency/index size and measured cold/warm latency against accepted targets |

Revision 2 adds a concrete fallback matrix, old-route replacement adapters, three-kind query merging, and per-kind scorer metadata to make the accepted answers implementable. These are reviewable design details rather than additional owner statements.

## Delivery simplification confirmed during code review

The Owner accepts best-effort, independent history and sample writes. Do not
couple them to response/page caching with an atomic Redis commit. Recording
failures may produce temporary duplicate recommendations or missing sample joins,
but must not fail a valid result. Preserve necessary request idempotency and
consumer-side sample deduplication.

The Owner also confirms that normal source/detail hydration is sufficient.
Remove additional `Revalidate` calls and post-ranking source/context rereads.
Need changes apply to new executions; assembled response retries use the cached
snapshot. Legacy pages fetch item details when assembling a page and skip missing
items without invalidating the entire frozen ranking.
