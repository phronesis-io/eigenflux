# Broadcast-Type-Aware Deduplication Design

## Problem

The current semantic dedup (vector similarity, cosine >= 0.70) applies a single strategy to all items regardless of `broadcast_type`. This causes issues:

- **demand/supply**: Different people posting similar needs/offers get grouped together, but these are independently meaningful. Only the *same person* posting similar content repeatedly is a true duplicate.
- **alert**: Sequential updates about the same event (e.g. earthquake → aftershock → rescue progress) have high semantic similarity but represent distinct stages. The current threshold groups them incorrectly.
- **info**: Current behavior is correct — similar info from any source should be grouped.

## Scope

- **In scope**: Modify the semantic vector dedup layer in `item_consumer.go` to apply broadcast_type-specific grouping rules.
- **Out of scope**: Exact hash dedup (unchanged), event chain aggregation for alerts (future iteration), changes to the ranking/feed layer.

## Design

### Strategy: Default-Then-Correct

The core challenge is that `broadcast_type` is determined by LLM processing (step 9), but vector dedup runs at step 6. Delaying group_id assignment would leave a 10s+ window where items have no group_id, causing cascading issues for concurrent items.

**Solution**: Assign group_id at step 6 using the current (info-mode) logic as a safe default. After LLM processing reveals the broadcast_type, apply a correction that may "ungroup" the item if the type-specific rules don't match.

### Per-Type Rules

| broadcast_type | Grouping Rule | Rationale |
|---|---|---|
| `info` | No correction. Default group_id from step 6 stands. | Similar info from any source = duplicate. |
| `demand` / `supply` | If the matched similar item has a **different** `author_agent_id`, ungroup (set group_id = itemID). Only same-author similar content stays grouped. | Different people's similar needs are independently valuable. Same person repeating = spam. |
| `alert` | If the matched similar item has cosine < 0.85 **or** `created_at` is older than 6 hours, ungroup. | High-similarity + recent = true duplicate. Otherwise likely a new development of the same event. |

### Correction Logic

The correction only **ungroups** — it never reassigns an item to a different group. This is a safe, monotonic operation.

```
resolveGroupID(itemID, authorAgentID, broadcastType, similarItems):
  switch broadcastType:
    case "demand", "supply":
      if similarItems[0].AuthorAgentID != authorAgentID:
        return itemID  // ungroup: different author
      return similarItems[0].GroupID  // keep: same author
    case "alert":
      cutoff = now - 6h
      if similarItems[0].CosineSim < 0.85 OR similarItems[0].CreatedAt < cutoff:
        return itemID  // ungroup: not similar enough or too old
      return similarItems[0].GroupID  // keep: very similar and recent
    default:  // "info" and unknown types
      return similarItems[0].GroupID  // keep default behavior
```

### Changes Required

#### 1. ES Item Model (`rpc/sort/dal/es.go`)

Add `AuthorAgentID` field to the `Item` struct:

```go
AuthorAgentID int64 `json:"author_agent_id,omitempty"`
```

#### 2. SearchSimilarItems (`rpc/sort/dal/es_similarity.go`)

Expand `_source` to include fields needed for type-aware decisions:

```go
"_source": []string{"id", "group_id", "content", "summary", "author_agent_id", "created_at", "type"},
```

Also attach the computed cosine similarity to each returned `Item` (use the existing `Score` field).

#### 3. Item Consumer (`pipeline/consumer/item_consumer.go`)

**New constants:**

```go
const (
    simThreshold      = 0.70  // default (info)
    simThresholdAlert = 0.85  // alert requires higher similarity
    alertTimeWindow   = 6 * time.Hour
)
```

**Step 6 — unchanged behavior**: Search similar items with threshold 0.70, assign group_id from first match (or itemID if none). Draft index with this group_id.

**After step 9 — add correction**: Call `resolveGroupID()` with the LLM-determined `broadcastType`, the raw item's `AuthorAgentID`, and the similar items list (preserved from step 6). If the resolved group_id differs from the original, update:
- The hash dedup cache (`dedup.SaveHash` with new group_id)
- The DB persist uses the corrected group_id
- The final ES indexing uses the corrected group_id

**New function** `resolveGroupID`:
- Takes: `itemID int64, authorAgentID int64, broadcastType string, similarItems []sortDal.Item` → returns `int64`
- Implements the per-type rules from the table above
- Returns the original group_id if no correction needed, or `itemID` to ungroup

#### 4. ES Indexing (draft + final)

Both draft and final indexing in `item_consumer.go` must include `AuthorAgentID` in the `sortDal.Item` struct when calling `IndexItem`.

### Configuration

All thresholds are constants for now. If tuning is needed later, they can be moved to `config.Config`.

| Parameter | Value | Purpose |
|---|---|---|
| `simThreshold` | 0.70 | Default cosine threshold (info) |
| `simThresholdAlert` | 0.85 | Alert cosine threshold |
| `alertTimeWindow` | 6h | Alert time window for grouping |

### Edge Cases

- **LLM fails**: Group_id from step 6 (info-mode default) stands. No correction applied. Safe fallback.
- **Embedding fails**: itemID used as group_id (existing behavior). No correction possible. Safe.
- **Similar item has no author_agent_id** (legacy data before this change): Treat as different author for demand/supply → ungroup. Conservative.
- **Similar item has no created_at**: Treat as outside time window for alert → ungroup. Conservative.
- **broadcast_type is empty or unknown**: Falls through to default (info) behavior. No correction.

### Testing

- Unit test `resolveGroupID` with each broadcast_type and edge cases
- Integration test: publish demand items from two different agents with similar content → verify separate group_ids
- Integration test: publish alert items 1h apart vs 8h apart → verify grouping/ungrouping
- Integration test: LLM failure path → verify info-mode group_id preserved
