# Rerank

## Overview

Item discovery uses three layers: recall produces candidate IDs, the typed item ranker scores them, and `rpc/sort/rerank` applies operator policies before final delivery. The rerank layer is owned by Sort and operates on the read-only `rank.Candidate` interface.

## Packages

`rpc/sort/rank` provides `Candidate`, `CandidateItem`, and `BasicCandidate`. The adapter keeps the typed source available while policies adjust scores or attach compact replay reasons.

`rpc/sort/rerank` provides the policy chain and the policies currently used by `SortItems`:

- `FreshnessPolicy` drops stale items according to type-specific YAML rules.
- `BoostPolicy` applies operator weights through `type`, `source_type`, and `content_class` rules plus optional per-item multipliers; a matching `item_id` skips all `boost_rules` for that item.
- `InjectPolicy` reserves delivery capacity for candidates from configured recall sources.
- `MatchLimitPolicy` caps candidates matching a configured recall-source predicate.

Policies are pure transforms with no I/O. Request-specific predicates and source maps are constructed by the Sort handler so the rank and rerank packages remain independent of recall implementations.

## Configuration

`SortItems` reads `configs/sort/rerank.yaml` once at startup. Missing or invalid configuration logs a warning and disables configured item policies for that process. Freshness runs before typed ranking, boost runs after ranking, injection runs before Bloom deduplication, and source limits run after Bloom deduplication but before final truncation.

`boost` accepts an optional `item_boosts` list of `{item_id, weight}` entries. IDs and weights must be positive, weights must be finite, and IDs must be unique. A matching item has its score multiplied by the configured weight and skips `boost_rules`; nonmatching items retain the existing attribute-rule behavior. The repository configuration omits `item_boosts`; operators add it only when production IDs and weights are required.

## Verification

Run `go test ./rpc/sort/rank/... ./rpc/sort/rerank/...` and build the Sort service after changing the candidate contract or policy configuration.
