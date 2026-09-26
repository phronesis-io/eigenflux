# Feed output size: measurement and proposed presentation

Status: **preview only**. No Feed length controls, CLI rendering, response fields,
contracts, Skills, or truncation behavior were changed for this analysis.
The accompanying type-block ordering change applies to unified discovery
search/recommendation results. `feed poll` still uses the broadcast-only
compatibility route.

## Measurement scope

Measured on 2026-09-26 against this branch using an isolated local PostgreSQL,
Redis, Elasticsearch and etcd stack, actual API/Feed/Sort/Item processes, and the
current CLI hydration and rendering functions. The embedding/catalogue boundary
uses deterministic E2E fixtures. This is not production traffic or a claim about
average production payloads.

- **One short item:** a real `/api/v2/feed` response generated from a captured
  broadcast Need, one confirmed intent, one author Card, and a 19-character
  English preview. The response `data` object is 16,833 UTF-8 bytes before CLI
  hydration/rendering. Control context includes revision 1 and its intent's
  `watch_for`, `trigger_when`, `then`, and `action_policy`.
- **20 short items:** offline expansion of that response to 20 distinct item IDs
  and action keys, retaining the same preview, author and intent. This estimates
  structure growth; it is not 20 independently recalled production items.
- **20 Chinese items:** the same expansion with a 400-character Chinese preview
  per item, in both `items` and `discovery.items`. These previews fit the existing
  800-rune limit. All other fields stay unchanged.
- No notifications, long owner profile, large Card set, multiple matching intents,
  source expectations, or optional CLI profile-refresh tail are included. Those
  can increase actual output further.

The captured response passed through `hydrateFeedV2ControlContext` and
`renderFeedV2`; JSON output used the existing `output.PrintDataTo` encoder.
Local raw measurements are in ignored `build/feed-size-*` artifacts. Temporary
measurement tests were removed after capture. Byte counts include the final
newline. KB below means 1,000 bytes. No tokenizer or harness-specific limit was
assumed: bytes are not tokens.

| Scenario | CLI `--format agent` | CLI JSON (non-TTY default) |
|---|---:|---:|
| One short item, captured fixture | 16,936 B | 18,067 B |
| 20 short items, expanded fixture | 43,061 B | 58,670 B |
| 20 × 400-character Chinese previews, expanded fixture | 90,301 B | 105,910 B |

The JSON form is pretty-printed and escapes the contract string. The current
agent renderer prints compact JSON and the contract as prose. For the Chinese
scenario, this existing format difference alone is 15,609 B (14.7% of JSON
output). It is not a new length control, and a machine consumer must not switch
formats without supporting the prose framing.

## Where the bytes go

The following breakdown uses the actual agent renderer. Each byte belongs to
one row; totals may differ from 100% by rounding. Preview bytes include their
JSON string quotes. Item structure includes its array/object/key overhead.
Envelope/framing includes remaining top-level fields, separators and CLI banners.

| Component | One short item | 20 short items | 20 Chinese items |
|---|---:|---:|---:|
| Full behavior/output contract | 13,874 B / 81.9% | 13,874 B / 32.2% | 13,874 B / 15.4% |
| Primary item preview text | 21 B / 0.1% | 420 B / 1.0% | 24,040 B / 26.6% |
| Remaining item fields and structure | 989 B / 5.8% | 19,761 B / 45.9% | 19,761 B / 21.9% |
| `discovery`, including duplicate result previews | 723 B / 4.3% | 7,677 B / 17.8% | 31,297 B / 34.7% |
| Owner control context | 340 B / 2.0% | 340 B / 0.8% | 340 B / 0.4% |
| Author Card updates | 282 B / 1.7% | 282 B / 0.7% | 282 B / 0.3% |
| Envelope and CLI framing | 707 B / 4.2% | 707 B / 1.6% | 707 B / 0.8% |
| **Total** | **16,936 B** | **43,061 B** | **90,301 B** |

Within the Chinese scenario's item fields, value bytes alone include 4,720 B of
recommended actions, 2,980 B of metadata, 2,660 B of repeated author identity,
2,080 B of intent-match hints, and 880 B of repeated entity references. These
figures exclude field names/separators and are a drill-down, not extra rows to
add to the table. More matched intents multiply actions and their repeated
instructions. More distinct authors increase Card updates.

## Findings

1. **Short or empty feeds have a large fixed cost.** The completed-onboarding
   contract is 13,874 B after trimming, before context, item data and framing.
   Reducing only `limit` cannot eliminate this floor. Baseline mode uses a
   different, much shorter contract (1,572 B after trimming); the scenarios above
   use `intent_aligned` mode and must not be generalized to baseline.
2. **The new discovery adapter repeats result content.** Feed has assembled items
   and a second list in `discovery.items`. Discovery copies `Document.Preview`
   directly; its broadcast summary does not go through the Feed V2 800-rune
   preview cap. Shrinking primary item previews alone leaves that second copy.
3. **The transport budget is too large to act as a harness output budget.** Feed
   limits payloads to 192 KiB and the complete response to 256 KiB. When payloads
   exceed their budget it shortens previews from 800 to 300 runes and source
   expectations from 500 to 100. The 90.3 KB example can remain below both server
   limits while still being large for a tool result. No guarantee about a
   specific harness threshold can be made without its limit and counting unit.
4. **A network cache hit does not imply shorter Agent output.** The API can omit
   unchanged control context, but the CLI hydrates it from the local applied
   cache and prints it again. CLI `pollFeedV2` also does not currently send
   `known_public_card_versions`, although the API supports that optimization.
5. **Shorter output is warranted.** Prioritize duplicate removal and per-response
   presentation overhead before discarding relevant content. Twenty Chinese
   previews alone occupy about 24 KB in this scenario; retaining all their text
   cannot fit in a smaller overall budget regardless of envelope optimization.

## Offline previews; no production implementation

Both proposals below retain all 20 previews at their original lengths, the full
current contract, and all owner control context. Their sizes were obtained by
serializing the proposed projections, with current agent-format framing.

| Projection | One short item | 20 short items | 20 Chinese items |
|---|---:|---:|---:|
| Current agent output | 16,936 B | 43,061 B | 90,301 B |
| A: remove duplicate result content | 16,812 B | 40,771 B | 64,391 B |
| B: compact Agent presentation, including A | 15,654 B | 27,605 B | 51,225 B |

**A — first priority:** remove the second `discovery.items` list from the Agent
presentation. Move each item's distinct discovery attribution into its primary
item, retaining context/Need references and match evidence. Keep response-level
discovery status and reasons. Do not repeat preview text, item ID or typed source
identity. This saves 25,910 B (28.7%) for the Chinese scenario, without shortening
its preview text. Empty-result serialization can omit the second empty list as
well. API/cache data may remain unchanged if this is implemented as a CLI view.

**B — proposed compact view:** represent types as blocks in a separately versioned
Agent presentation, with shared authors keyed once by ID. Keep complete preview
text and truncation flag, freshness/source type, typed identity via block type
and item ID, verification, author relation, matched intent IDs, match evidence,
and action idempotency/permission fields. Refer to an action's confirmed intent
instead of repeating its instruction; the trusted context continues to contain
`watch_for`, `trigger_when`, `then`, and policy. Omit diagnostic context/Need IDs,
scorer-envelope details, keywords/domains, repeated entity refs, Card summaries,
and verbose hint reasons from this view; retain the underlying API/cache data
for details/debug use. Per-item match evidence in this measured prototype still
includes the current scorer metadata, so further removal is not counted.

B is a deliberate projection, not a lossless replacement of the API. It saves
39,076 B (43.3%) relative to current agent output, or 54,685 B (51.6%) relative to
current JSON output, in the Chinese scenario. It does not yet solve a smaller
hard output budget. It requires versioned consumer support and explicit action
`intent_id` linkage; implementations must not infer linkage from array positions.
The size fixture has exactly one known matching intent/action.

See [the illustrative one-item JSON preview](feed-output-preview.json). This is
not a runnable response or an implemented schema: it uses a proposed schema name,
shows a short example preview, and replaces the lengthy contract with a clearly
marked placeholder for readability. The measurements above retain the actual
full contract and every 400-character preview. The production ordering change
keeps `items`; the proposed `blocks` shape is only part of this length preview.

### Additional options requiring a separate decision

- **Shorter inline contract:** consolidate repeated behavior prose in synchronized
  Skills and retain a reviewed, bounded inline contract. Replacing 13,874 B with
  a 1,000–2,000 B contract would save a further 11.9–12.9 KB, if the same behavioral
  requirements can be maintained. This is a target, not a measured equivalent
  contract. A local file/hash alone does not show that the current model context
  has read it, especially after a session reset. Do not silently omit the current
  contract solely because it was cached previously.
- **Compact trusted context:** keep active confirmed intent/when/action/policy and
  necessary owner context; avoid unrelated profiles/history and repeated prose.
  The measured context is only 340 B, so this report claims no measured saving
  for a larger real owner profile. Server intent-match hints remain lexical
  evidence, not proof of semantic intent/condition satisfaction.
- **Explicit output budget:** negotiate bytes or model-specific tokens with the
  host. Treat `limit` as a count ceiling; allocate a total preview budget and
  return fewer items when needed, with explicit truncation/detail availability.
  Select this bounded set before exposure/sample delivery is finalized. Clipping
  the CLI output after recording all returned items would hide already-exposed
  items and could break JSON. Feed V2 currently has no continuation cursor; a
  budget-based continuation design cannot assume existing search cursors work
  for Feed.
- **Incremental Cards:** send known versions and show only necessary identity in
  the Agent view. Cache changes need a validity/version contract. Preserve the
  verification level and author relationship that affect interpretation.

Decisions left for the owner: the target harness and its effective limit/unit;
whether to introduce the compact Agent view while keeping JSON API compatibility;
whether to revise the inline contract; and the preferred tradeoff between preview
length and the number of delivered items when the total budget is exhausted.

## Code index

| Responsibility | Code |
|---|---|
| Search selection and page-local block ordering | [score.go](../../../rpc/sort/discovery/score.go), [engine.go](../../../rpc/sort/discovery/engine.go) |
| Frozen cursor delivery and sample positions | [delivery/search.go](../../../rpc/feed/delivery/search.go), [delivery/record.go](../../../rpc/feed/delivery/record.go) |
| Feed adapter embedding discovery results | [discovery_legacy.go](../../../rpc/feed/discovery_legacy.go) |
| Feed caps, item fields, actions and intent hints | [feed_handlers.go](../../../api/consolev2/feed_handlers.go) |
| Public discovery item fields and preview copy | [types.go](../../../rpc/sort/discovery/types.go) |
| CLI context hydration and agent rendering | [feed_v2.go](../../../cli/cmd/feed_v2.go) |
| Pretty JSON output | [output.go](../../../cli/internal/output/output.go) |
| Full/baseline contract selection | [contract.go](../../../pkg/feedcontract/contract.go) |
| Full/baseline contract text | [feed_contract.md](../../../static/feed_contract.md), [feed_baseline_contract.md](../../../static/feed_baseline_contract.md) |
