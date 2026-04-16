# Broadcast-Type-Aware Deduplication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the semantic vector dedup layer broadcast_type-aware: demand/supply only group same-author items, alert uses higher threshold + time window, info unchanged.

**Architecture:** "Default-then-correct" — step 6 assigns group_id using current info-mode logic as safe default. After LLM processing reveals broadcast_type, a `resolveGroupID` function corrects the group_id by ungrouping items that don't meet type-specific rules. Corrections only ungroup (set group_id = itemID), never reassign to a different group.

**Tech Stack:** Go, Elasticsearch (kNN + _source fields), Redis (hash dedup cache)

---

### Task 1: Add `AuthorAgentID` to ES Item model

**Files:**
- Modify: `rpc/sort/dal/es.go:17-39`

- [ ] **Step 1: Add field to Item struct**

In `rpc/sort/dal/es.go`, add `AuthorAgentID` field to the `Item` struct after the `ID` field (line 18):

```go
type Item struct {
	ID               int64                  `json:"id"`
	AuthorAgentID    int64                  `json:"author_agent_id,omitempty"`
	Content          string                 `json:"content"`
	// ... rest unchanged
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./rpc/sort/...`
Expected: Build succeeds with no errors.

- [ ] **Step 3: Commit**

```bash
git add rpc/sort/dal/es.go
git commit -m "feat(dedup): add AuthorAgentID to ES Item model"
```

---

### Task 2: Expand `SearchSimilarItems` to return dedup-relevant fields

**Files:**
- Modify: `rpc/sort/dal/es_similarity.go:12-96`

- [ ] **Step 1: Expand `_source` fields in the kNN query**

In `SearchSimilarItems`, change the `_source` array (line 34) to include fields needed for type-aware dedup:

```go
"_source": []string{"id", "group_id", "content", "summary", "author_agent_id", "created_at", "type"},
```

- [ ] **Step 2: Populate `Score` on returned items**

After unmarshaling each hit into `item`, set the cosine similarity score so the caller can use it for threshold checks. Add after line 91 (`json.Unmarshal`), before `items = append`:

```go
item.Score = float64(cosineSim)
```

- [ ] **Step 3: Verify build**

Run: `go build ./rpc/sort/...`
Expected: Build succeeds with no errors.

- [ ] **Step 4: Commit**

```bash
git add rpc/sort/dal/es_similarity.go
git commit -m "feat(dedup): expand SearchSimilarItems to return author, created_at, type, and cosine score"
```

---

### Task 3: Write `resolveGroupID` function with tests (TDD)

**Files:**
- Create: `pipeline/consumer/dedup.go`
- Create: `pipeline/consumer/dedup_test.go`

- [ ] **Step 1: Write failing tests for `resolveGroupID`**

Create `pipeline/consumer/dedup_test.go`:

```go
package consumer

import (
	"testing"
	"time"

	sortDal "eigenflux_server/rpc/sort/dal"

	"github.com/stretchr/testify/assert"
)

func TestResolveGroupID_Info_KeepsExistingGroup(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "info", similar)
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Info_NoSimilar_ReturnsItemID(t *testing.T) {
	got := resolveGroupID(1, 111, "info", nil)
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Demand_SameAuthor_KeepsGroup(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 111, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "demand", similar)
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Demand_DifferentAuthor_Ungroups(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "demand", similar)
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Supply_SameAuthor_KeepsGroup(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 111, Score: 0.80, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "supply", similar)
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Supply_DifferentAuthor_Ungroups(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.80, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "supply", similar)
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Alert_HighSimRecent_KeepsGroup(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.90, CreatedAt: time.Now().Add(-1 * time.Hour)},
	}
	got := resolveGroupID(1, 111, "alert", similar)
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Alert_LowSim_Ungroups(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.75, CreatedAt: time.Now().Add(-1 * time.Hour)},
	}
	got := resolveGroupID(1, 111, "alert", similar)
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Alert_OldItem_Ungroups(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.90, CreatedAt: time.Now().Add(-8 * time.Hour)},
	}
	got := resolveGroupID(1, 111, "alert", similar)
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_UnknownType_KeepsGroup(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "", similar)
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Demand_NoSimilar_ReturnsItemID(t *testing.T) {
	got := resolveGroupID(1, 111, "demand", nil)
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Demand_SameAuthor_ZeroAuthorID_Ungroups(t *testing.T) {
	// Legacy data: similar item has no author_agent_id (zero value)
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 0, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "demand", similar)
	assert.Equal(t, int64(1), got, "legacy items with zero author_agent_id should be treated as different author")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pipeline/consumer/ -run TestResolveGroupID -v`
Expected: Compilation error — `resolveGroupID` undefined.

- [ ] **Step 3: Implement `resolveGroupID`**

Create `pipeline/consumer/dedup.go`:

```go
package consumer

import (
	"time"

	sortDal "eigenflux_server/rpc/sort/dal"
)

const (
	simThresholdAlert = 0.85
	alertTimeWindow   = 6 * time.Hour
)

// resolveGroupID applies broadcast_type-specific rules to correct the default
// group_id assigned during the initial (info-mode) vector dedup.
// It only ungroups (returns itemID) — never reassigns to a different group.
func resolveGroupID(itemID, authorAgentID int64, broadcastType string, similarItems []sortDal.Item) int64 {
	if len(similarItems) == 0 {
		return itemID
	}

	best := similarItems[0]

	switch broadcastType {
	case "demand", "supply":
		if best.AuthorAgentID != authorAgentID || best.AuthorAgentID == 0 {
			return itemID
		}
		return best.GroupID

	case "alert":
		cutoff := time.Now().Add(-alertTimeWindow)
		if best.Score < simThresholdAlert || best.CreatedAt.Before(cutoff) {
			return itemID
		}
		return best.GroupID

	default:
		return best.GroupID
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pipeline/consumer/ -run TestResolveGroupID -v`
Expected: All 12 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add pipeline/consumer/dedup.go pipeline/consumer/dedup_test.go
git commit -m "feat(dedup): add resolveGroupID with broadcast_type-specific rules"
```

---

### Task 4: Integrate `resolveGroupID` into item consumer pipeline

**Files:**
- Modify: `pipeline/consumer/item_consumer.go:126-442`

This task modifies `processMessage` in three places:
1. Preserve `similarItems` from step 6
2. Include `AuthorAgentID` in draft/final ES indexing
3. After LLM, call `resolveGroupID` to correct group_id

- [ ] **Step 1: Preserve similarItems from vector dedup phase**

In `processMessage`, change the vector dedup block (lines 210-226) to preserve the similar items list for later correction. Replace:

```go
	// Phase 2: Vector-based deduplication (assigns group_id, does not discard)
	if itemEmbedding == nil {
		logger.Default().Warn("ItemConsumer all embedding attempts failed, using item_id as group_id", "itemID", itemID)
		finalGroupID = itemID
	} else {
		similarItems, err := sortDal.SearchSimilarItems(ctx, itemEmbedding, simThreshold, 5)
		if err != nil {
			logger.Default().Warn("ItemConsumer similarity search failed", "itemID", itemID, "err", err)
			finalGroupID = itemID
		} else if len(similarItems) > 0 {
			finalGroupID = similarItems[0].GroupID
			logger.Default().Info("ItemConsumer item matched to group", "itemID", itemID, "groupID", finalGroupID, "similarItemID", similarItems[0].ID)
		} else {
			finalGroupID = itemID
			logger.Default().Info("ItemConsumer item is unique, creating new group", "itemID", itemID, "groupID", finalGroupID)
		}
	}
```

With:

```go
	// Phase 2: Vector-based deduplication (assigns default group_id using info-mode rules)
	// similarItems is preserved for post-LLM broadcast_type-specific correction
	var similarItems []sortDal.Item
	if itemEmbedding == nil {
		logger.Default().Warn("ItemConsumer all embedding attempts failed, using item_id as group_id", "itemID", itemID)
		finalGroupID = itemID
	} else {
		var err error
		similarItems, err = sortDal.SearchSimilarItems(ctx, itemEmbedding, simThreshold, 5)
		if err != nil {
			logger.Default().Warn("ItemConsumer similarity search failed", "itemID", itemID, "err", err)
			finalGroupID = itemID
		} else if len(similarItems) > 0 {
			finalGroupID = similarItems[0].GroupID
			logger.Default().Info("ItemConsumer item matched to group (default)", "itemID", itemID, "groupID", finalGroupID, "similarItemID", similarItems[0].ID)
		} else {
			finalGroupID = itemID
			logger.Default().Info("ItemConsumer item is unique, creating new group", "itemID", itemID, "groupID", finalGroupID)
		}
	}
```

- [ ] **Step 2: Add `AuthorAgentID` to draft indexing**

Change the draft item construction (lines 262-270) to include `AuthorAgentID`:

```go
	draftItem := &sortDal.Item{
		ID:            itemID,
		AuthorAgentID: raw.AuthorAgentID,
		Content:       raw.RawContent,
		RawURL:        raw.RawURL,
		GroupID:       finalGroupID,
		Embedding:     itemEmbedding,
		CreatedAt:     time.Unix(raw.CreatedAt/1000, 0),
		UpdatedAt:     time.Now(),
	}
```

- [ ] **Step 3: Add group_id correction after LLM processing**

After the quality check passes (after line 322, the "item passed quality check" log), add the group_id correction logic. Insert before the `domainsStr` construction (line 324):

```go
	// Correct group_id based on broadcast_type-specific rules
	correctedGroupID := resolveGroupID(itemID, raw.AuthorAgentID, result.BroadcastType, similarItems)
	if correctedGroupID != finalGroupID {
		logger.Default().Info("ItemConsumer group_id corrected by broadcast_type rules",
			"itemID", itemID, "broadcastType", result.BroadcastType,
			"oldGroupID", finalGroupID, "newGroupID", correctedGroupID)
		finalGroupID = correctedGroupID
		// Update hash dedup cache with corrected group_id
		if err := dedup.SaveHash(ctx, mq.RDB, contentHash, finalGroupID); err != nil {
			logger.Default().Warn("ItemConsumer failed to update hash after group correction", "itemID", itemID, "err", err)
		}
	}
```

- [ ] **Step 4: Add `AuthorAgentID` to final ES indexing**

Change the final ES item construction (lines 345-364) to include `AuthorAgentID`:

```go
	esItem := &sortDal.Item{
		ID:               itemID,
		AuthorAgentID:    raw.AuthorAgentID,
		Content:          raw.RawContent,
		RawURL:           raw.RawURL,
		Summary:          result.Summary,
		Type:             result.BroadcastType,
		Domains:          result.Domains,
		Keywords:         result.Keywords,
		ExpireTime:       parseExpireTime(result.ExpireTime),
		Geo:              result.Geo,
		SourceType:       result.SourceType,
		ExpectedResponse: finalExpectedResponse,
		GroupID:          finalGroupID,
		QualityScore:     result.Quality,
		Lang:             result.Lang,
		Timeliness:       result.Timeliness,
		Embedding:        itemEmbedding,
		CreatedAt:        time.Unix(raw.CreatedAt/1000, 0),
		UpdatedAt:        time.Now(),
	}
```

- [ ] **Step 5: Verify build**

Run: `go build ./pipeline/...`
Expected: Build succeeds.

- [ ] **Step 6: Run existing unit tests**

Run: `go test ./pipeline/consumer/ -v`
Expected: All existing tests still pass (the `persistProcessedItem` tests are unaffected).

- [ ] **Step 7: Commit**

```bash
git add pipeline/consumer/item_consumer.go
git commit -m "feat(dedup): integrate resolveGroupID into item processing pipeline"
```

---

### Task 5: Update documentation

**Files:**
- Modify: `docs/dev/pipeline.md`

- [ ] **Step 1: Update pipeline docs to describe broadcast_type-aware dedup**

Read `docs/dev/pipeline.md` and find the section describing the dedup/grouping step. Add a subsection describing the new behavior:

```markdown
### Broadcast-Type-Aware Group Correction

After LLM processing determines the `broadcast_type`, the default group_id (assigned using info-mode rules) is corrected:

| broadcast_type | Rule | Rationale |
|---|---|---|
| `info` | No correction | Similar info from any source = duplicate |
| `demand` / `supply` | Ungroup if matched item has different `author_agent_id` | Different people's similar needs are independently valuable |
| `alert` | Ungroup if cosine < 0.85 or matched item older than 6h | Sequential event updates should not be grouped |

Constants: `simThresholdAlert = 0.85`, `alertTimeWindow = 6h` (in `pipeline/consumer/dedup.go`).
```

- [ ] **Step 2: Commit**

```bash
git add docs/dev/pipeline.md
git commit -m "docs: describe broadcast_type-aware dedup correction"
```

---

### Task 6: Build, start services, and run tests

**Files:** None (validation only)

- [ ] **Step 1: Build all services**

Run: `bash scripts/common/build.sh`
Expected: Build succeeds.

- [ ] **Step 2: Start services and run tests**

Run: `./scripts/local/start_local.sh` then `go test -v ./tests/...`
Expected: All tests pass.

- [ ] **Step 3: Run consumer unit tests specifically**

Run: `go test ./pipeline/consumer/ -v`
Expected: All tests pass including the new `TestResolveGroupID_*` tests.
