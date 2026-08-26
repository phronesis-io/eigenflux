# Item ID Whitelist Boost Design

## Problem

The Sort rerank layer can currently boost candidates only by item attributes (`type`, `source_type`, and `content_class`). Operators need to heat a specific set of item IDs through `configs/sort/rerank.yaml` without changing code.

## Decision

Extend the existing `boost` policy with item-specific boost entries. Keeping item and attribute boosts in one policy gives that policy sole ownership of boost precedence and requires only one score update and one stable sort.

A dedicated whitelist policy was rejected because the existing attribute boost policy would need cross-policy state to detect and skip already-whitelisted candidates. Handler-level logic was rejected because it would move operator scoring behavior outside the rerank strategy layer.

## Configuration Contract

The `boost` policy accepts an optional `item_boosts` list:

```yaml
policies:
  - name: boost
    item_boosts:
      - item_id: 123456
        weight: 2.0
      - item_id: 789012
        weight: 1.5
    boost_rules:
      - field: type
        values: [supply, demand]
        weight: 1.3
```

Each entry has two required fields:

- `item_id`: a positive signed 64-bit item ID.
- `weight`: a positive finite multiplier.

Duplicate `item_id` entries are invalid. Rejecting duplicates avoids implicit last-write-wins or accidental multiplicative behavior.

The repository configuration will declare `item_boosts: []`. Production item IDs and weights remain operator-owned and are not invented by this change.

## Runtime Behavior

`BoostPolicy` remains a post-rank policy. It runs after typed ranking and before the relevance-threshold split, allowing a heated item to cross the delivery threshold.

For each mutable item candidate:

1. Look up the candidate ID in the configured item boost table.
2. When found, multiply the current score by that item weight, add reason `boost:item_id=<id>`, and skip all attribute `boost_rules` for that candidate.
3. When not found, apply existing matching attribute rules multiplicatively without behavior changes.
4. Stable-sort all candidates by descending final score.

Whitelist precedence is therefore override precedence within the boost stage: an item whitelist multiplier replaces all type, source-type, and content-class multipliers for that candidate. It does not replace the upstream ranker score.

Non-item candidates, immutable `Candidate` implementations, and empty configurations continue to pass through unchanged.

## Components

### Configuration

`rpc/sort/rerank/config.go` owns YAML decoding and validation. It will add an item boost configuration type, validate IDs and weights, reject duplicate IDs, and construct the runtime lookup table.

### Policy

`rpc/sort/rerank/policy_boost.go` owns whitelist precedence and score mutation. The lookup is keyed by `int64`, giving constant-time matching per candidate without scanning every configured ID.

### Sort integration

No new handler path is required. `applyPostRankBoost` already wraps ranked items as `rank.Candidate`, executes `BoostPolicy`, copies final scores back into `ranker.RankedItem`, and preserves the policy-produced order.

## Error Handling

Invalid item boost configuration makes `Config.NewPolicies` return an error. Existing startup behavior remains unchanged: `loadRerankPolicySet` logs a warning and disables configured rerank policies for that process. No partial whitelist is accepted.

## Verification

Tests will cover:

- decoding item boosts from YAML;
- rejection of non-positive IDs, non-positive or non-finite weights, and duplicate IDs;
- item whitelist score multiplication and stable reordering;
- whitelist precedence over matching attribute boost rules;
- unchanged attribute boost behavior for non-whitelisted items;
- successful loading of `configs/sort/rerank.yaml` with an empty whitelist.

Focused verification will run the rerank and rank package tests, the Sort service tests covering policy wiring, and the Sort service build.
