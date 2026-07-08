# Rerank

## Overview

Discovery surfaces in eigenflux follow a three-layer pipeline:

1. **Recall** — domain-specific channels (`pkg/recallsource`, ES queries, kNN) produce raw candidate IDs.
2. **Rank** — domain rankers (`rpc/sort/ranker` for items, `rpc/sort/serviceranker` for services) score those candidates with typed signals and emit typed results (`RankedItem`, `RankedService`).
3. **Rerank** — `rpc/sort/rerank` mixes typed results into the final display order, applying cross-type policies (dedup, normalize, bounds, ratio, slot).

The first two layers stay domain-typed for clarity and performance. The rerank layer is the only place that has to be cross-type, so it operates on a small read-only interface (`rank.Candidate`) wrapped around whatever the typed ranker produced.

The rerank layer lives inside the sort service because sort owns feed composition. When other services need to feed candidates into the same surface, they produce typed ranker results and hand them to sort; they do not run rerank themselves.

## Packages

### `rpc/sort/rank`

- `Candidate` interface — `ID() int64`, `Type() CandidateType`, `Score() float64`, `Features() map[string]float64`, `Source() any`, `Fingerprint() string`. Read-only; mutation happens on the concrete adapter.
- `CandidateType` constants — `CandidateItem`, `CandidateService`. Add new constants when wiring a new typed ranker into rerank.
- `BasicCandidate` adapter — wraps a typed ranker result via `rank.NewCandidate(id, type, score, features, source)`. Mutators (`SetScore`, `AddReason`) live on the concrete type so the interface stays read-only.
- `Fingerprint` defaults to `"<type>:<id>"`, used by `DedupPolicy` and reachable by future "already shown" filters.
- Distinct from `recallsource.Candidate`, which represents a single item returned by one recall channel and lives a layer below.

### `rpc/sort/rerank`

- `Reranker` — built with `rerank.New(policies...)`; `Rerank(cands, limit)` runs the policies in order, then truncates.
- `Policy` interface — `Name() string`, `Apply([]rank.Candidate) []rank.Candidate`. Implementations must be pure (no I/O, no goroutines).
- Eight built-in policies:
  - **FreshnessPolicy** — drops item candidates by type-aware age rules loaded from `configs/sort/rerank.yaml`. The default rule is `broadcast_type: alert`, `max_age: 6h`, `action: drop`. `SortItems` applies this policy after recall and before typed item ranking/exploration so stale alerts cannot re-enter through exploration slots.
  - **BoostPolicy** — multiplies item candidate scores by operator-tuned category weights, then re-sorts by descending score. Each `BoostRule` matches a source field (`type` = broadcast_type, `source_type`, or `content_class` = `ugc`/`pgc`) against a value set and applies `Weight`; multiple matching rules compound (e.g. a UGC demand item hit by both `type ∈ {supply,demand}` ×1.3 and `content_class ∈ {ugc}` ×1.2 lands at ×1.56). `content_class` is resolved per request in `SortItems` from the author's email suffix (PGC = official bots ending in `@bot.eigenflux.one` / `@pgc.eigenflux.one`, configurable via `PGC_EMAIL_SUFFIXES`; everything else, including unresolved authors, is UGC) and carried on the boost source — it is not stored on the ES document. Only mutates `*BasicCandidate`; services and unknown sources pass through. `SortItems` applies this policy *after* item ranking (so score edits survive) and *before* the relevance threshold split (so a boosted item can cross into the served set). Configured under `configs/sort/rerank.yaml`. Reads category fields via the `ItemBoostFields()` source interface.
  - **DedupPolicy** — drops duplicates by `Fingerprint`. Put it first.
  - **NormalizePolicy** — rescales scores per type. `Method: MinMax` maps each type group to `[0, 1]`; `Method: ZScore` standardises. Only mutates `*BasicCandidate`; other Candidate implementations pass through.
  - **CoveragePolicy** — guarantees each *intent* (string label off `*BasicCandidate.MatchedIntents()`) appears at least `FloorPerIntent` times in the top-`Limit` window. Iterates protected intents alphabetically; for each, swaps the highest-scoring outside-window match in for the lowest-scoring inside-window non-match, locking the satisfied slot to prevent later intents from evicting it. `Importance` (intent → [0,1]) filters which intents are protected — those below `ImportanceThreshold` drift naturally on score. Used by `SearchServices`. Does NOT call `AddReason` (intent-name cardinality would pollute replay aggregation).
  - **BoundsPolicy** — `Bounds map[CandidateType]Bound{Floor, Ceiling}` plus `Limit int`. Ceiling > 0 trims to the top-N by score per type. When `Limit > 0`, Floor is enforced by tail-replacement: after the ceiling pass, for each type with `Floor` greater than its count in the first `Limit` positions, the lowest-scoring non-matching slot inside the window is swapped for the highest-scoring matching candidate outside the window. With `Limit <= 0` Floor degrades to informational (the policy cannot fabricate candidates that recall did not return).
  - **RatioPolicy** — `CycleSize` and `TypeCounts` describe the target interleave (e.g. `{item: 5, service: 1}` over a cycle of 6). Underflow falls through to whichever queue still has candidates.
  - **SlotPolicy** — pins specific 0-indexed positions to a target type. Top-scoring unused candidate of that type is promoted. Place this last so positional overrides win over interleave rhythm.

The canonical composition for `SearchServices` is `Dedup → Normalize{MinMax} → Coverage → Bounds{Ceiling: 10}`. `SortItems` additionally has a configurable pre-rank item policy chain loaded from `configs/sort/rerank.yaml`, currently used for freshness hard limits. See `docs/dev/sort.md` for the surrounding handler flow.

### YAML Configuration

`SortItems` reads `configs/sort/rerank.yaml` once during Sort startup. A missing or invalid file logs a warning and disables configured item rerank policies for that process.

`SortItems` splits configured policies into a pre-rank stage (freshness drops, applied to recall candidates) and a post-rank stage (boosts, applied to ranked items before the relevance threshold split).

```yaml
policies:
  - name: freshness
    item_rules:
      - broadcast_type: alert
        max_age: 6h
        action: drop
  - name: boost
    boost_rules:
      - field: type
        values: [supply, demand]
        weight: 1.3
      - field: content_class
        values: [ugc]   # non-PGC-bot authors
        weight: 1.2
```

### Canonical Composition

```go
import (
    "eigenflux_server/rpc/sort/rank"
    "eigenflux_server/rpc/sort/rerank"
)

mixer := rerank.New(
    &rerank.DedupPolicy{},
    &rerank.NormalizePolicy{Method: rerank.MinMax},
    &rerank.BoundsPolicy{
        Limit: 20, // floor window — typically the caller-requested result limit
        Bounds: map[rank.CandidateType]rerank.Bound{
            rank.CandidateItem:    {Floor: 0, Ceiling: 30},
            rank.CandidateService: {Floor: 1, Ceiling: 3},
        },
    },
    &rerank.RatioPolicy{CycleSize: 6, TypeCounts: map[rank.CandidateType]int{
        rank.CandidateItem:    5,
        rank.CandidateService: 1,
    }},
    &rerank.SlotPolicy{Slots: []rerank.SlotRule{
        {Position: 2, Type: rank.CandidateService},
    }},
)
final := mixer.Rerank(allCandidates, 20)
```

### Wrapping Typed Ranker Results

The boundary code converts each typed ranker output into a `BasicCandidate`. The original typed struct stays reachable via `Source()`.

```go
// items: []ranker.RankedItem from rpc/sort/ranker.Rank
// services: []serviceranker.RankedService from rpc/sort/serviceranker.Rank

candidates := make([]rank.Candidate, 0, len(items)+len(services))
for i := range items {
    ri := &items[i]
    candidates = append(candidates, rank.NewCandidate(
        ri.ItemID,
        rank.CandidateItem,
        ri.Score,
        map[string]float64{
            "semantic":  ri.Scores.Semantic,
            "keyword":   ri.Scores.Keyword,
            "freshness": ri.Scores.Freshness,
        },
        ri,
    ))
}
for i := range services {
    rs := &services[i]
    candidates = append(candidates, rank.NewCandidate(
        rs.ServiceID,
        rank.CandidateService,
        rs.Score,
        map[string]float64{
            "semantic": rs.Breakdown.Semantic,
            "success":  rs.Breakdown.Success,
            "latency":  rs.Breakdown.Latency,
        },
        rs,
    ))
}

final := mixer.Rerank(candidates, limit)

// Recover the typed payload after rerank.
for _, c := range final {
    switch c.Type() {
    case rank.CandidateItem:
        ri := c.Source().(*ranker.RankedItem)
        _ = ri
    case rank.CandidateService:
        rs := c.Source().(*serviceranker.RankedService)
        _ = rs
    }
}
```

## Status

`SearchServices` uses the rerank chain for multi-intent service ranking. `SortItems` uses `FreshnessPolicy` from `configs/sort/rerank.yaml` before item ranking, and uses the mixed item/service rerank chain when `ENABLE_SERVICE_MIX=true`.

## Verification

- `go build ./...` — all packages including the new ones compile clean.
- `go test ./rpc/sort/rank/... ./rpc/sort/rerank/...` — unit tests cover every policy and the reranker composition.
- `go vet ./rpc/sort/rank/... ./rpc/sort/rerank/...`, `gofmt -l rpc/sort/rank rpc/sort/rerank` — clean.
