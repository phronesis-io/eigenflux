# Offline Replay Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a standalone Hertz HTTP service that simulates the full sort pipeline with customizable parameters and simulated time, returning ranked items with score breakdowns for external LLM-as-judge evaluation.

**Architecture:** Standalone service (`replay/`) on port 8092 that directly imports `rpc/sort/ranker` and `rpc/sort/dal` packages. Orchestrates recall → rank → dedup → threshold → exploration without side effects (no bloom filter writes, no cache, no impression recording). Three `time.Now()` call sites need refactoring to accept a `now` parameter.

**Tech Stack:** Go, Hertz HTTP framework, Elasticsearch (read-only), PostgreSQL (read-only profile lookup), Redis (read-only bloom filter when `use_feed_history: true`)

**Spec:** `docs/superpowers/specs/2026-04-20-offline-replay-design.md`

---

## File Structure

| File | Responsibility |
|---|---|
| `replay/main.go` | Hertz server on :8092, init ES client + DB + optional Redis (for bloom filter), route registration |
| `replay/handler.go` | Request/response types, HTTP handler, request validation |
| `replay/pipeline.go` | Sort pipeline orchestration: profile resolution → parallel recall → rank → dedup → threshold → exploration → bloom filter |
| `replay/config.go` | Parse request params, merge with prod defaults from `RankerConfig` |
| `replay/scripts/build.sh` | Build script |
| `replay/scripts/start.sh` | Start script |
| `rpc/sort/dal/es_query.go` | Modify: `buildExpireTimeFilter` and `BuildRecallFilters` accept `now time.Time` |
| `rpc/sort/ranker/ranker.go` | Modify: `Rank` and `rankMMR` accept `now time.Time` parameter |
| `rpc/sort/ranker/exploration.go` | Modify: `PickExplorationItems` accepts `now time.Time` parameter |

---

### Task 1: Refactor `time.Now()` out of `buildExpireTimeFilter` and `BuildRecallFilters`

Currently `buildExpireTimeFilter()` (es_query.go:23) calls `time.Now()` internally. The replay service needs to inject a simulated timestamp.

**Files:**
- Modify: `rpc/sort/dal/es_query.go:23-47` (`buildExpireTimeFilter`), `rpc/sort/dal/es_query.go:75-81` (`BuildRecallFilters`)
- Modify: `rpc/sort/dal/es_query.go:84` (`buildSearchQuery` — caller of `buildExpireTimeFilter`)
- Modify: `rpc/sort/dal/es.go:100` (`SearchItems` — caller of `buildSearchQuery`)
- Modify: `rpc/sort/handler.go:215` (caller of `BuildRecallFilters`)
- Test: `rpc/sort/dal/es_query_test.go`

- [ ] **Step 1: Write failing test for `buildExpireTimeFilter` with custom time**

In `rpc/sort/dal/es_query_test.go`, add:

```go
func TestBuildExpireTimeFilterCustomTime(t *testing.T) {
	fixedTime := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	filter := buildExpireTimeFilter(fixedTime)

	// Verify the filter uses the fixed time, not time.Now()
	boolClause := filter["bool"].(map[string]interface{})
	shouldClauses := boolClause["should"].([]interface{})
	rangeClause := shouldClauses[1].(map[string]interface{})
	expireRange := rangeClause["range"].(map[string]interface{})
	expireTime := expireRange["expire_time"].(map[string]interface{})
	gte := expireTime["gte"].(string)

	expected := fixedTime.Format(time.RFC3339)
	if gte != expected {
		t.Errorf("expected expire filter time %s, got %s", expected, gte)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/misaki/go/eigenflux && go test -v -run TestBuildExpireTimeFilterCustomTime ./rpc/sort/dal/`
Expected: compilation error — `buildExpireTimeFilter` doesn't accept a `time.Time` parameter yet.

- [ ] **Step 3: Refactor `buildExpireTimeFilter` to accept `now time.Time`**

In `rpc/sort/dal/es_query.go`, change line 23 from:

```go
func buildExpireTimeFilter() map[string]interface{} {
```

to:

```go
func buildExpireTimeFilter(now time.Time) map[string]interface{} {
```

And change line 39 from:

```go
"gte": time.Now().Format(time.RFC3339),
```

to:

```go
"gte": now.Format(time.RFC3339),
```

- [ ] **Step 4: Update `BuildRecallFilters` to accept and pass `now`**

In `rpc/sort/dal/es_query.go`, change line 75 from:

```go
func BuildRecallFilters(geoCountry string) []interface{} {
	filters := []interface{}{buildExpireTimeFilter()}
```

to:

```go
func BuildRecallFilters(geoCountry string, now time.Time) []interface{} {
	filters := []interface{}{buildExpireTimeFilter(now)}
```

- [ ] **Step 5: Update `buildSearchQuery` to accept and pass `now`**

In `rpc/sort/dal/es_query.go`, the `buildSearchQuery` function calls `buildExpireTimeFilter()`. Add a `Now` field to `SearchItemsRequest`:

In `rpc/sort/dal/es.go`, add to the `SearchItemsRequest` struct (after line 77):

```go
Now time.Time // Simulated "now" for expire filter and decay origin; zero means use time.Now()
```

In `rpc/sort/dal/es_query.go` line 84, `buildSearchQuery` already receives `req *SearchItemsRequest`. At the call to `buildExpireTimeFilter` (around line 104), change:

```go
buildExpireTimeFilter()
```

to:

```go
buildExpireTimeFilter(req.Now)
```

- [ ] **Step 6: Default `Now` to `time.Now()` in `SearchItems` for backward compatibility**

In `rpc/sort/dal/es.go`, at the start of `SearchItems` (line 100), add:

```go
if req.Now.IsZero() {
	req.Now = time.Now()
}
```

- [ ] **Step 7: Update handler.go caller of `BuildRecallFilters`**

In `rpc/sort/handler.go` line 215, change:

```go
filters := sortDal.BuildRecallFilters("")
```

to:

```go
filters := sortDal.BuildRecallFilters("", time.Now())
```

- [ ] **Step 8: Run tests to verify everything passes**

Run: `cd /Users/misaki/go/eigenflux && go test -v ./rpc/sort/dal/ && go build ./rpc/sort/`
Expected: all tests pass, build succeeds.

- [ ] **Step 9: Commit**

```bash
git add rpc/sort/dal/es_query.go rpc/sort/dal/es.go rpc/sort/dal/es_query_test.go rpc/sort/handler.go
git commit -m "refactor(sort/dal): accept now parameter in expire filter and recall filters

Thread simulated time through buildExpireTimeFilter and BuildRecallFilters
to support offline replay with custom timestamps."
```

---

### Task 2: Refactor `time.Now()` out of `Ranker.Rank` and `PickExplorationItems`

The ranker's `Rank` method (ranker.go:50) and `PickExplorationItems` (exploration.go:17) each capture `time.Now()` internally. The replay service needs to pass a simulated timestamp.

**Files:**
- Modify: `rpc/sort/ranker/ranker.go:45-89` (`Rank`), `rpc/sort/ranker/ranker.go:93-140` (`rankMMR`)
- Modify: `rpc/sort/ranker/exploration.go:12-71` (`PickExplorationItems`)
- Modify: `rpc/sort/handler.go:397` (caller of `Rank`), `rpc/sort/handler.go:427` (caller of `PickExplorationItems`)
- Test: `rpc/sort/ranker/ranker_test.go`, `rpc/sort/ranker/exploration_test.go`

- [ ] **Step 1: Write failing test for `Rank` with custom time**

In `rpc/sort/ranker/ranker_test.go`, add:

```go
func TestRankWithCustomTime(t *testing.T) {
	cfg := &RankerConfig{
		Alpha: 0.0, Beta: 0.0, Gamma: 1.0, Delta: 0.0,
		Freshness: map[string]FreshnessParams{
			"info": {Offset: 12 * time.Hour, Scale: 7 * 24 * time.Hour, Decay: 0.8},
		},
		DraftDampening: 0.8,
	}
	r := New(cfg)

	now := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	candidates := []sortDal.Item{
		{ID: 1, Type: "info", Keywords: []string{"go"}, UpdatedAt: now.Add(-1 * time.Hour)},
		{ID: 2, Type: "info", Keywords: []string{"go"}, UpdatedAt: now.Add(-48 * time.Hour)},
	}
	profile := &UserProfile{Keywords: []string{"go"}}

	ranked := r.RankAt(candidates, profile, 2, now)

	if len(ranked) != 2 {
		t.Fatalf("expected 2 ranked items, got %d", len(ranked))
	}
	// Item 1 is 1h old, item 2 is 48h old — item 1 should score higher on freshness
	if ranked[0].ItemID != 1 {
		t.Errorf("expected item 1 ranked first (fresher), got item %d", ranked[0].ItemID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/misaki/go/eigenflux && go test -v -run TestRankWithCustomTime ./rpc/sort/ranker/`
Expected: compilation error — `RankAt` method doesn't exist yet.

- [ ] **Step 3: Add `RankAt` method and refactor `Rank` to call it**

In `rpc/sort/ranker/ranker.go`, rename the existing `Rank` internals into `RankAt` which accepts `now`, then make `Rank` a wrapper:

```go
func (r *Ranker) Rank(candidates []sortDal.Item, profile *UserProfile, limit int) []RankedItem {
	return r.RankAt(candidates, profile, limit, time.Now())
}

func (r *Ranker) RankAt(candidates []sortDal.Item, profile *UserProfile, limit int, now time.Time) []RankedItem {
	if len(candidates) == 0 {
		return nil
	}

	ps := buildProfileSets(profile)

	type scored struct {
		idx    int
		score  float64
		scores ScoreBreakdown
	}
	items := make([]scored, len(candidates))
	for i, item := range candidates {
		bd := r.scoreItem(item, profile, ps, now)
		items[i] = scored{idx: i, score: bd.Total, scores: bd}
	}

	for i := 0; i < len(items) && i < limit; i++ {
		best := i
		for j := i + 1; j < len(items); j++ {
			if items[j].score > items[best].score {
				best = j
			}
		}
		items[i], items[best] = items[best], items[i]
	}

	if len(items) > limit {
		items = items[:limit]
	}

	selected := make([]RankedItem, len(items))
	for i, s := range items {
		selected[i] = RankedItem{
			ItemID: candidates[s.idx].ID,
			Score:  s.score,
			Scores: s.scores,
		}
	}
	return selected
}
```

- [ ] **Step 4: Add `PickExplorationItemsAt` and refactor `PickExplorationItems`**

In `rpc/sort/ranker/exploration.go`:

```go
func PickExplorationItems(candidates []sortDal.Item, seenIDs, seenGroupIDs map[int64]bool, count int, maxAge time.Duration, minQuality float64) []sortDal.Item {
	return PickExplorationItemsAt(candidates, seenIDs, seenGroupIDs, count, maxAge, minQuality, time.Now())
}

func PickExplorationItemsAt(candidates []sortDal.Item, seenIDs, seenGroupIDs map[int64]bool, count int, maxAge time.Duration, minQuality float64, now time.Time) []sortDal.Item {
	if count <= 0 || len(candidates) == 0 {
		return nil
	}

	cutoff := now.Add(-maxAge)

	var eligible []sortDal.Item
	for _, item := range candidates {
		if item.Type == "" {
			continue
		}
		if item.QualityScore < minQuality {
			continue
		}
		if item.UpdatedAt.Before(cutoff) {
			continue
		}
		if seenIDs[item.ID] {
			continue
		}
		if item.GroupID != 0 && seenGroupIDs[item.GroupID] {
			continue
		}
		eligible = append(eligible, item)
	}

	for i := 0; i < len(eligible) && i < count; i++ {
		best := i
		for j := i + 1; j < len(eligible); j++ {
			if eligible[j].QualityScore > eligible[best].QualityScore {
				best = j
			}
		}
		eligible[i], eligible[best] = eligible[best], eligible[i]
	}

	result := make([]sortDal.Item, 0, count)
	selectedGroupIDs := make(map[int64]bool, len(seenGroupIDs)+count)
	for groupID, seen := range seenGroupIDs {
		if seen {
			selectedGroupIDs[groupID] = true
		}
	}
	for _, item := range eligible {
		if item.GroupID != 0 && selectedGroupIDs[item.GroupID] {
			continue
		}
		if item.GroupID != 0 {
			selectedGroupIDs[item.GroupID] = true
		}
		result = append(result, item)
		if len(result) >= count {
			break
		}
	}
	return result
}
```

- [ ] **Step 5: Run all ranker tests**

Run: `cd /Users/misaki/go/eigenflux && go test -v ./rpc/sort/ranker/ && go build ./rpc/sort/`
Expected: all tests pass including the new `TestRankWithCustomTime`, build succeeds. Existing callers are unaffected because `Rank` and `PickExplorationItems` still work with their original signatures.

- [ ] **Step 6: Commit**

```bash
git add rpc/sort/ranker/ranker.go rpc/sort/ranker/exploration.go rpc/sort/ranker/ranker_test.go
git commit -m "refactor(ranker): add RankAt and PickExplorationItemsAt with custom time

Add time-parameterized variants of Rank and PickExplorationItems for
offline replay. Original methods preserved as wrappers for backward
compatibility."
```

---

### Task 3: Create replay service skeleton (`main.go` + `config.go`)

**Files:**
- Create: `replay/main.go`
- Create: `replay/config.go`

- [ ] **Step 1: Create `replay/config.go`**

This file parses the optional request overrides and merges them with prod defaults.

```go
package main

import (
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/rpc/sort/ranker"
)

// ReplayRankerParams are optional overrides from the request.
// Pointer types: nil means "use prod default".
type ReplayRankerParams struct {
	Alpha             *float64                       `json:"alpha,omitempty"`
	Beta              *float64                       `json:"beta,omitempty"`
	Gamma             *float64                       `json:"gamma,omitempty"`
	Delta             *float64                       `json:"delta,omitempty"`
	MinRelevanceScore *float64                       `json:"min_relevance_score,omitempty"`
	UrgencyBoost      *float64                       `json:"urgency_boost,omitempty"`
	UrgencyWindow     *string                        `json:"urgency_window,omitempty"`
	ExplorationSlots  *int                           `json:"exploration_slots,omitempty"`
	DraftDampening    *float64                       `json:"draft_dampening,omitempty"`
	Freshness         map[string]ReplayFreshnessParams `json:"freshness,omitempty"`
}

type ReplayFreshnessParams struct {
	Offset *string  `json:"offset,omitempty"`
	Scale  *string  `json:"scale,omitempty"`
	Decay  *float64 `json:"decay,omitempty"`
}

type ReplayRecallParams struct {
	KeywordRecallSize  *int  `json:"keyword_recall_size,omitempty"`
	EnableKNNRecall    *bool `json:"enable_knn_recall,omitempty"`
	KNNRecallK         *int  `json:"knn_recall_k,omitempty"`
	KNNRecallCandidates *int `json:"knn_recall_candidates,omitempty"`
}

// mergeRankerConfig builds a RankerConfig from prod defaults, overriding with
// any non-nil fields from the request.
func mergeRankerConfig(base *ranker.RankerConfig, override *ReplayRankerParams) *ranker.RankerConfig {
	merged := *base // shallow copy
	if override == nil {
		return &merged
	}
	if override.Alpha != nil {
		merged.Alpha = *override.Alpha
	}
	if override.Beta != nil {
		merged.Beta = *override.Beta
	}
	if override.Gamma != nil {
		merged.Gamma = *override.Gamma
	}
	if override.Delta != nil {
		merged.Delta = *override.Delta
	}
	if override.MinRelevanceScore != nil {
		merged.MinRelevanceScore = *override.MinRelevanceScore
	}
	if override.UrgencyBoost != nil {
		merged.UrgencyBoost = *override.UrgencyBoost
	}
	if override.UrgencyWindow != nil {
		merged.UrgencyWindow = parseDurationWithDefault(*override.UrgencyWindow, merged.UrgencyWindow)
	}
	if override.ExplorationSlots != nil {
		merged.ExplorationSlots = *override.ExplorationSlots
	}
	if override.DraftDampening != nil {
		merged.DraftDampening = *override.DraftDampening
	}
	if override.Freshness != nil {
		freshness := make(map[string]ranker.FreshnessParams)
		for k, v := range merged.Freshness {
			freshness[k] = v
		}
		for k, v := range override.Freshness {
			fp := freshness[k]
			if v.Offset != nil {
				fp.Offset = parseDurationWithDefault(*v.Offset, fp.Offset)
			}
			if v.Scale != nil {
				fp.Scale = parseDurationWithDefault(*v.Scale, fp.Scale)
			}
			if v.Decay != nil {
				fp.Decay = *v.Decay
			}
			freshness[k] = fp
		}
		merged.Freshness = freshness
	}
	return &merged
}

func mergeRecallParams(cfg *config.Config, override *ReplayRecallParams) (keywordRecallSize int, enableKNN bool, knnK int, knnCandidates int) {
	keywordRecallSize = cfg.KeywordRecallSize
	enableKNN = cfg.EnableKNNRecall
	knnK = cfg.KNNRecallK
	knnCandidates = cfg.KNNRecallCandidates
	if override == nil {
		return
	}
	if override.KeywordRecallSize != nil {
		keywordRecallSize = *override.KeywordRecallSize
	}
	if override.EnableKNNRecall != nil {
		enableKNN = *override.EnableKNNRecall
	}
	if override.KNNRecallK != nil {
		knnK = *override.KNNRecallK
	}
	if override.KNNRecallCandidates != nil {
		knnCandidates = *override.KNNRecallCandidates
	}
	return
}

func parseDurationWithDefault(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		for _, ch := range s[:len(s)-1] {
			if ch < '0' || ch > '9' {
				return fallback
			}
			days = days*10 + int(ch-'0')
		}
		return time.Duration(days) * 24 * time.Hour
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}
```

- [ ] **Step 2: Create `replay/main.go`**

```go
package main

import (
	"context"
	"log"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/telemetry"
	"eigenflux_server/rpc/sort/ranker"
)

var (
	cfg        *config.Config
	bf         *bloomfilter.BloomFilter
	baseCfg    *ranker.RankerConfig
)

func main() {
	cfg = config.Load()
	logFlush := logger.Init("ReplayService", cfg.EffectiveLokiURL(), cfg.LogLevel)
	defer logFlush()

	shutdown, err := telemetry.Init("ReplayService", cfg.OtelExporterEndpoint, cfg.MonitorEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	// PostgreSQL for profile lookups
	db.Init(cfg.PgDSN)

	// Redis for bloom filter (read-only)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)
	bf = bloomfilter.NewBloomFilter(mq.RDB)

	// Ranker config from prod env vars (used as defaults)
	baseCfg = ranker.NewRankerConfig(cfg)

	// Elasticsearch
	if err := es.InitES(cfg.EmbeddingDimensions); err != nil {
		log.Fatalf("failed to initialize ES: %v", err)
	}

	listenAddr := cfg.ListenAddr(cfg.ReplayPort)
	h := server.Default(server.WithHostPorts(listenAddr))

	h.POST("/api/v1/replay/sort", handleReplaySort)

	// Health check
	h.GET("/health", func(ctx context.Context, c *app.RequestContext) {
		c.JSON(consts.StatusOK, map[string]string{"status": "ok"})
	})

	logger.Default().Info("replay service started", "addr", listenAddr)
	h.Spin()
}
```

- [ ] **Step 3: Add `ReplayPort` to config**

In `pkg/config/config.go`, add `ReplayPort int` field to the `Config` struct (next to the other port fields), and in `Load()` add:

```go
ReplayPort: getEnvInt("REPLAY_PORT", 8092),
```

- [ ] **Step 4: Verify build compiles**

Run: `cd /Users/misaki/go/eigenflux && go build ./replay/`
Expected: compilation error — `handleReplaySort` not defined yet. This is expected; we'll create it in the next task.

- [ ] **Step 5: Commit**

```bash
git add replay/main.go replay/config.go pkg/config/config.go
git commit -m "feat(replay): add service skeleton with config merge logic

Standalone Hertz service on port 8092 with param override merging.
Handler implementation follows in next commit."
```

---

### Task 4: Implement request/response types and handler (`handler.go`)

**Files:**
- Create: `replay/handler.go`

- [ ] **Step 1: Create `replay/handler.go` with types and handler**

```go
package main

import (
	"context"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"eigenflux_server/pkg/json"
	"eigenflux_server/pkg/logger"
)

type ReplayRequest struct {
	AgentID        int64               `json:"agent_id"`
	AgentProfile   *ReplayAgentProfile `json:"agent_profile,omitempty"`
	SimulatedAt    *string             `json:"simulated_at,omitempty"`
	UseFeedHistory bool                `json:"use_feed_history"`
	Limit          int                 `json:"limit,omitempty"`
	RankerParams   *ReplayRankerParams `json:"ranker_params,omitempty"`
	RecallParams   *ReplayRecallParams `json:"recall_params,omitempty"`
}

type ReplayAgentProfile struct {
	Keywords  []string  `json:"keywords,omitempty"`
	Domains   []string  `json:"domains,omitempty"`
	Geo       string    `json:"geo,omitempty"`
	Embedding []float32 `json:"embedding,omitempty"`
}

type ReplayResponse struct {
	RankedItems      []ReplayItem          `json:"ranked_items"`
	FilteredItems    []ReplayItem          `json:"filtered_items"`
	ExplorationItems []ReplayItem          `json:"exploration_items"`
	AgentProfile     ReplayProfileSummary  `json:"agent_profile"`
	ConfigUsed       ReplayConfigSummary   `json:"config_used"`
	Stats            ReplayStats           `json:"stats"`
}

type ReplayItem struct {
	ItemID   int64                  `json:"item_id"`
	Position int                    `json:"position"`
	Score    float64                `json:"score"`
	Scores   map[string]interface{} `json:"scores"`
	Item     map[string]interface{} `json:"item"`
}

type ReplayProfileSummary struct {
	Keywords     []string `json:"keywords"`
	Domains      []string `json:"domains"`
	Geo          string   `json:"geo"`
	HasEmbedding bool     `json:"has_embedding"`
}

type ReplayConfigSummary struct {
	RankerParams map[string]interface{} `json:"ranker_params"`
	RecallParams map[string]interface{} `json:"recall_params"`
}

type ReplayStats struct {
	KeywordRecallCount   int   `json:"keyword_recall_count"`
	KNNRecallCount       int   `json:"knn_recall_count"`
	MergedCount          int   `json:"merged_count"`
	AfterGroupDedupCount int   `json:"after_group_dedup_count"`
	AboveThresholdCount  int   `json:"above_threshold_count"`
	BloomFilteredCount   int   `json:"bloom_filtered_count"`
	TotalLatencyMs       int64 `json:"total_latency_ms"`
}

func handleReplaySort(ctx context.Context, c *app.RequestContext) {
	start := time.Now()

	var req ReplayRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
		return
	}

	if req.AgentID <= 0 {
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "agent_id is required"})
		return
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}

	// Parse simulated time
	now := time.Now()
	if req.SimulatedAt != nil && *req.SimulatedAt != "" {
		parsed, err := time.Parse(time.RFC3339, *req.SimulatedAt)
		if err != nil {
			c.JSON(consts.StatusBadRequest, map[string]string{"error": "invalid simulated_at format, use RFC3339: " + err.Error()})
			return
		}
		now = parsed
	}

	// Merge configs
	rankerCfg := mergeRankerConfig(baseCfg, req.RankerParams)
	keywordRecallSize, enableKNN, knnK, knnCandidates := mergeRecallParams(cfg, req.RecallParams)

	logger.Ctx(ctx).Info("replay request",
		"agentID", req.AgentID,
		"simulatedAt", now,
		"useFeedHistory", req.UseFeedHistory,
		"limit", limit,
	)

	// Run pipeline
	result, err := runReplayPipeline(ctx, &pipelineParams{
		agentID:           req.AgentID,
		agentProfile:      req.AgentProfile,
		now:               now,
		useFeedHistory:    req.UseFeedHistory,
		limit:             limit,
		rankerCfg:         rankerCfg,
		keywordRecallSize: keywordRecallSize,
		enableKNN:         enableKNN,
		knnK:              knnK,
		knnCandidates:     knnCandidates,
	})
	if err != nil {
		logger.Ctx(ctx).Error("replay pipeline failed", "err", err)
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	result.Stats.TotalLatencyMs = time.Since(start).Milliseconds()

	body, _ := json.Marshal(result)
	c.Data(consts.StatusOK, "application/json", body)
}
```

- [ ] **Step 2: Verify it compiles (will fail — `runReplayPipeline` not yet defined)**

Run: `cd /Users/misaki/go/eigenflux && go build ./replay/`
Expected: compilation error for `runReplayPipeline`. This is expected.

- [ ] **Step 3: Commit**

```bash
git add replay/handler.go
git commit -m "feat(replay): add request/response types and HTTP handler

Parses replay request, merges config overrides, delegates to pipeline."
```

---

### Task 5: Implement replay pipeline (`pipeline.go`)

This is the core logic — profile resolution, parallel recall, ranking, dedup, threshold, exploration, and optional bloom filter read.

**Files:**
- Create: `replay/pipeline.go`

- [ ] **Step 1: Create `replay/pipeline.go`**

```go
package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pkg/db"
	embcodec "eigenflux_server/pkg/embedding/codec"
	"eigenflux_server/pkg/logger"
	profileDal "eigenflux_server/rpc/profile/dal"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/ranker"
)

type pipelineParams struct {
	agentID           int64
	agentProfile      *ReplayAgentProfile
	now               time.Time
	useFeedHistory    bool
	limit             int
	rankerCfg         *ranker.RankerConfig
	keywordRecallSize int
	enableKNN         bool
	knnK              int
	knnCandidates     int
}

func runReplayPipeline(ctx context.Context, p *pipelineParams) (*ReplayResponse, error) {
	// 1. Resolve agent profile
	keywords, domains, geo, profileEmbedding, err := resolveProfile(ctx, p.agentID, p.agentProfile)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve profile: %w", err)
	}

	profileSummary := ReplayProfileSummary{
		Keywords:     keywords,
		Domains:      domains,
		Geo:          geo,
		HasEmbedding: len(profileEmbedding) > 0,
	}

	// 2. Parallel ES recall
	var keywordItems []sortDal.Item
	var knnItems []sortDal.Item
	var keywordErr, knnErr error
	var wg sync.WaitGroup

	// Keyword recall
	wg.Add(1)
	go func() {
		defer wg.Done()
		searchReq := &sortDal.SearchItemsRequest{
			Limit:           p.keywordRecallSize,
			Domains:         domains,
			Keywords:        keywords,
			Geo:             geo,
			FreshnessOffset: cfg.FreshnessOffset,
			FreshnessScale:  cfg.FreshnessScale,
			FreshnessDecay:  cfg.FreshnessDecay,
			Now:             p.now,
		}
		resp, err := sortDal.SearchItems(ctx, searchReq)
		if err != nil {
			keywordErr = err
			return
		}
		keywordItems = resp.Items
	}()

	// kNN recall
	if p.enableKNN && len(profileEmbedding) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			filters := sortDal.BuildRecallFilters("", p.now)
			knnItems, knnErr = sortDal.SearchByEmbedding(ctx, profileEmbedding, filters, p.knnK, p.knnCandidates)
			if knnErr != nil {
				logger.Ctx(ctx).Warn("kNN recall failed", "err", knnErr)
			}
		}()
	}

	wg.Wait()
	if keywordErr != nil {
		return nil, fmt.Errorf("keyword recall failed: %w", keywordErr)
	}

	stats := ReplayStats{
		KeywordRecallCount: len(keywordItems),
		KNNRecallCount:     len(knnItems),
	}

	// 3. Merge and deduplicate recall results
	esItems := keywordItems
	seen := make(map[int64]bool, len(esItems))
	for _, item := range esItems {
		seen[item.ID] = true
	}
	if knnErr == nil {
		for _, item := range knnItems {
			if !seen[item.ID] {
				esItems = append(esItems, item)
				seen[item.ID] = true
			}
		}
	}
	stats.MergedCount = len(esItems)

	// 4. Timestamp filter: only items updated at or before simulated_at
	filtered := make([]sortDal.Item, 0, len(esItems))
	for _, item := range esItems {
		if !item.UpdatedAt.After(p.now) {
			filtered = append(filtered, item)
		}
	}
	esItems = filtered

	// Build item map
	esItemMap := make(map[int64]sortDal.Item, len(esItems))
	for _, item := range esItems {
		esItemMap[item.ID] = item
	}

	// 5. Rank with custom config and simulated time
	r := ranker.New(p.rankerCfg)
	userProfile := &ranker.UserProfile{
		Keywords:  keywords,
		Domains:   domains,
		Geo:       geo,
		Embedding: profileEmbedding,
	}
	allRanked := r.RankAt(esItems, userProfile, len(esItems), p.now)

	// 6. Group dedup (collapse by group_id)
	allRanked, _ = collapseRankedByGroup(allRanked, esItemMap)
	stats.AfterGroupDedupCount = len(allRanked)

	// 7. Relevance threshold
	ranked := make([]ranker.RankedItem, 0, len(allRanked))
	belowThreshold := make([]ranker.RankedItem, 0)
	for _, ri := range allRanked {
		if ri.Score >= p.rankerCfg.MinRelevanceScore {
			ranked = append(ranked, ri)
		} else {
			belowThreshold = append(belowThreshold, ri)
		}
	}
	stats.AboveThresholdCount = len(ranked)

	// 8. Exploration slots
	var explorationRanked []ranker.RankedItem
	if p.rankerCfg.ExplorationSlots > 0 {
		rankedIDs := make(map[int64]bool, len(ranked))
		rankedGroupIDs := make(map[int64]bool, len(ranked))
		for _, ri := range ranked {
			rankedIDs[ri.ItemID] = true
			if item, ok := esItemMap[ri.ItemID]; ok && item.GroupID != 0 {
				rankedGroupIDs[item.GroupID] = true
			}
		}
		explorationItems := ranker.PickExplorationItemsAt(esItems, rankedIDs, rankedGroupIDs, p.rankerCfg.ExplorationSlots, 48*time.Hour, 0.5, p.now)
		for _, ei := range explorationItems {
			explorationRanked = append(explorationRanked, ranker.RankedItem{ItemID: ei.ID, Score: 0.0})
		}
	}

	// 9. Bloom filter dedup (read-only, only if use_feed_history)
	bloomFiltered := 0
	seenGroupIDs := make(map[int64]bool)
	if p.useFeedHistory && bf != nil {
		allGroupIDs := make([]int64, 0)
		for _, ri := range ranked {
			if item, ok := esItemMap[ri.ItemID]; ok && item.GroupID != 0 {
				allGroupIDs = append(allGroupIDs, item.GroupID)
			}
		}
		if len(allGroupIDs) > 0 {
			seenMap, err := bf.CheckExists(ctx, p.agentID, allGroupIDs)
			if err != nil {
				logger.Ctx(ctx).Warn("bloom filter check failed", "err", err)
			} else {
				seenGroupIDs = seenMap
			}
		}
	}

	// 10. Build response items
	rankedResponse := make([]ReplayItem, 0, p.limit)
	pos := 0
	for _, ri := range ranked {
		item, ok := esItemMap[ri.ItemID]
		if !ok {
			continue
		}
		if item.GroupID != 0 && seenGroupIDs[item.GroupID] {
			bloomFiltered++
			continue
		}
		rankedResponse = append(rankedResponse, buildReplayItem(item, ri, pos))
		pos++
		if len(rankedResponse) >= p.limit {
			break
		}
	}
	stats.BloomFilteredCount = bloomFiltered

	filteredResponse := make([]ReplayItem, 0, len(belowThreshold))
	for i, ri := range belowThreshold {
		if item, ok := esItemMap[ri.ItemID]; ok {
			filteredResponse = append(filteredResponse, buildReplayItem(item, ri, i))
		}
	}

	explorationResponse := make([]ReplayItem, 0, len(explorationRanked))
	for i, ri := range explorationRanked {
		if item, ok := esItemMap[ri.ItemID]; ok {
			explorationResponse = append(explorationResponse, buildReplayItem(item, ri, i))
		}
	}

	return &ReplayResponse{
		RankedItems:      rankedResponse,
		FilteredItems:    filteredResponse,
		ExplorationItems: explorationResponse,
		AgentProfile:     profileSummary,
		ConfigUsed:       buildConfigSummary(p),
		Stats:            stats,
	}, nil
}

func resolveProfile(ctx context.Context, agentID int64, override *ReplayAgentProfile) (keywords, domains []string, geo string, embedding []float32, err error) {
	// If fully specified in request, skip DB
	if override != nil && len(override.Keywords) > 0 && len(override.Domains) > 0 {
		keywords = override.Keywords
		domains = override.Domains
		geo = override.Geo
		embedding = override.Embedding
		return
	}

	// Load from DB
	ap, dbErr := profileDal.GetAgentProfile(db.DB, agentID)
	if dbErr != nil {
		if override != nil {
			// Partial override with no DB profile — use what we have
			keywords = override.Keywords
			domains = override.Domains
			geo = override.Geo
			embedding = override.Embedding
			return
		}
		err = fmt.Errorf("agent %d profile not found: %w", agentID, dbErr)
		return
	}

	// Parse DB profile
	if ap.Keywords != "" && ap.Status == 3 {
		kws := strings.Split(ap.Keywords, ",")
		for _, kw := range kws {
			kw = strings.TrimSpace(kw)
			if kw != "" {
				keywords = append(keywords, kw)
			}
		}
		domains = keywords
	}
	if len(ap.ProfileEmbedding) > 0 {
		embedding = embcodec.Decode(ap.ProfileEmbedding)
	}

	// Apply overrides on top of DB values
	if override != nil {
		if len(override.Keywords) > 0 {
			keywords = override.Keywords
		}
		if len(override.Domains) > 0 {
			domains = override.Domains
		}
		if override.Geo != "" {
			geo = override.Geo
		}
		if len(override.Embedding) > 0 {
			embedding = override.Embedding
		}
	}
	return
}

// collapseRankedByGroup keeps the best-scored item per group_id.
// Copied from rpc/sort/handler.go to avoid importing the handler package.
func collapseRankedByGroup(ranked []ranker.RankedItem, itemMap map[int64]sortDal.Item) ([]ranker.RankedItem, int) {
	if len(ranked) == 0 {
		return nil, 0
	}
	collapsed := make([]ranker.RankedItem, 0, len(ranked))
	seenGroupIDs := make(map[int64]bool, len(ranked))
	seenItemIDs := make(map[int64]bool, len(ranked))
	filtered := 0
	for _, ri := range ranked {
		if seenItemIDs[ri.ItemID] {
			filtered++
			continue
		}
		item, ok := itemMap[ri.ItemID]
		if !ok {
			collapsed = append(collapsed, ri)
			seenItemIDs[ri.ItemID] = true
			continue
		}
		if item.GroupID != 0 {
			if seenGroupIDs[item.GroupID] {
				filtered++
				continue
			}
			seenGroupIDs[item.GroupID] = true
		}
		seenItemIDs[ri.ItemID] = true
		collapsed = append(collapsed, ri)
	}
	return collapsed, filtered
}

func buildReplayItem(item sortDal.Item, ri ranker.RankedItem, position int) ReplayItem {
	itemData := map[string]interface{}{
		"content":        item.Content,
		"summary":        item.Summary,
		"broadcast_type": item.Type,
		"keywords":       item.Keywords,
		"domains":        item.Domains,
		"geo":            item.Geo,
		"source_type":    item.SourceType,
		"quality_score":  item.QualityScore,
		"group_id":       item.GroupID,
		"lang":           item.Lang,
		"timeliness":     item.Timeliness,
		"updated_at":     item.UpdatedAt.Format(time.RFC3339),
		"created_at":     item.CreatedAt.Format(time.RFC3339),
	}
	if item.ExpireTime != nil {
		itemData["expire_time"] = item.ExpireTime.Format(time.RFC3339)
	}
	return ReplayItem{
		ItemID:   item.ID,
		Position: position,
		Score:    ri.Score,
		Scores: map[string]interface{}{
			"semantic":  ri.Scores.Semantic,
			"keyword":   ri.Scores.Keyword,
			"freshness": ri.Scores.Freshness,
			"total":     ri.Scores.Total,
			"is_draft":  ri.Scores.IsDraft,
		},
		Item: itemData,
	}
}

func buildConfigSummary(p *pipelineParams) ReplayConfigSummary {
	return ReplayConfigSummary{
		RankerParams: map[string]interface{}{
			"alpha":               p.rankerCfg.Alpha,
			"beta":                p.rankerCfg.Beta,
			"gamma":               p.rankerCfg.Gamma,
			"delta":               p.rankerCfg.Delta,
			"min_relevance_score": p.rankerCfg.MinRelevanceScore,
			"urgency_boost":       p.rankerCfg.UrgencyBoost,
			"urgency_window":      p.rankerCfg.UrgencyWindow.String(),
			"exploration_slots":   p.rankerCfg.ExplorationSlots,
			"draft_dampening":     p.rankerCfg.DraftDampening,
		},
		RecallParams: map[string]interface{}{
			"keyword_recall_size":  p.keywordRecallSize,
			"enable_knn_recall":    p.enableKNN,
			"knn_recall_k":         p.knnK,
			"knn_recall_candidates": p.knnCandidates,
		},
	}
}
```

- [ ] **Step 2: Verify full build**

Run: `cd /Users/misaki/go/eigenflux && go build ./replay/`
Expected: build succeeds.

- [ ] **Step 3: Commit**

```bash
git add replay/pipeline.go
git commit -m "feat(replay): implement sort pipeline with time simulation

Profile resolution (request override or DB), parallel keyword+kNN recall,
ranking with custom params, group dedup, threshold, exploration, and
optional read-only bloom filter dedup."
```

---

### Task 6: Add build and start scripts

**Files:**
- Create: `replay/scripts/build.sh`
- Create: `replay/scripts/start.sh`
- Modify: `scripts/common/build.sh`
- Modify: `scripts/local/start_local.sh`

- [ ] **Step 1: Create `replay/scripts/build.sh`**

```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

GO_CMD="go"
if command -v mise &>/dev/null; then
  GO_CMD="mise exec -- go"
fi

mkdir -p build
$GO_CMD build -o build/replay ./replay/
echo "Built: build/replay"
```

- [ ] **Step 2: Create `replay/scripts/start.sh`**

```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

source .env 2>/dev/null || true

REPLAY_PORT="${REPLAY_PORT:-8092}"

# Kill existing process on port
PID=$(lsof -ti :"$REPLAY_PORT" 2>/dev/null || true)
if [ -n "$PID" ]; then
  kill "$PID" 2>/dev/null || true
  sleep 0.5
fi

mkdir -p .log
nohup ./build/replay > .log/replay.log 2>&1 &

echo "Replay service starting on :$REPLAY_PORT (PID $!)"
echo "Logs: .log/replay.log"
```

- [ ] **Step 3: Make scripts executable**

Run: `chmod +x /Users/misaki/go/eigenflux/replay/scripts/build.sh /Users/misaki/go/eigenflux/replay/scripts/start.sh`

- [ ] **Step 4: Add `replay` to `scripts/common/build.sh` service list**

In `scripts/common/build.sh`, find the `ALL_SERVICES` array and add `replay` to it.

- [ ] **Step 5: Add `replay` to `scripts/local/start_local.sh`**

In `scripts/local/start_local.sh`, add `replay` with port 8092 to the `SERVICE_MAP` array.

- [ ] **Step 6: Commit**

```bash
git add replay/scripts/ scripts/common/build.sh scripts/local/start_local.sh
git commit -m "feat(replay): add build and start scripts

Add replay to common build list and local startup."
```

---

### Task 7: Integration test

**Files:**
- Create: `tests/replay/replay_test.go`
- Create: `tests/replay/main_test.go`

- [ ] **Step 1: Create `tests/replay/main_test.go`**

```go
package replay_test

import (
	"os"
	"testing"

	"eigenflux_server/tests/testutil"
)

func TestMain(m *testing.M) {
	testutil.InitDB()
	testutil.WaitForAPI(nil)
	code := testutil.RunTestMain(m)
	os.Exit(code)
}
```

- [ ] **Step 2: Create `tests/replay/replay_test.go`**

```go
package replay_test

import (
	"fmt"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

func TestReplaySortBasic(t *testing.T) {
	ts := time.Now().UnixNano()

	// Register a test agent with profile
	authorEmail := fmt.Sprintf("replay_author%d@test.com", ts)
	authorResult := testutil.RegisterAgent(t, authorEmail, "ReplayAuthor", "AI and distributed systems expert in tech and finance")
	authorToken := authorResult["token"].(string)

	userEmail := fmt.Sprintf("replay_user%d@test.com", ts)
	userResult := testutil.RegisterAgent(t, userEmail, "ReplayUser", "Interested in AI, machine learning, and tech startups")
	userID := testutil.MustID(t, userResult, "agent_id")

	// Publish some test items
	item1 := testutil.PublishItem(t, authorToken, "AI startup raises $50M for machine learning platform", "Great opportunity in tech", "")
	item1ID := testutil.MustID(t, item1, "item_id")

	item2 := testutil.PublishItem(t, authorToken, "New distributed database released for cloud native applications", "Interesting tech", "")
	item2ID := testutil.MustID(t, item2, "item_id")

	// Wait for items to be processed and indexed
	testutil.WaitForItemsProcessed(t, []int64{item1ID, item2ID})
	testutil.RefreshES(t)

	// Wait for user profile to be processed
	testutil.WaitForProfileProcessed(t, userID)

	// Test 1: Basic replay with defaults
	body := fmt.Sprintf(`{"agent_id": %d}`, userID)
	resp := testutil.DoPostRaw(t, "http://localhost:8092/api/v1/replay/sort", body, "")
	if resp == nil {
		t.Fatal("replay request failed")
	}

	rankedItems, ok := resp["ranked_items"].([]interface{})
	if !ok {
		t.Fatal("expected ranked_items array in response")
	}
	t.Logf("basic replay returned %d ranked items", len(rankedItems))

	// Verify response structure
	if _, ok := resp["stats"]; !ok {
		t.Error("expected stats in response")
	}
	if _, ok := resp["config_used"]; !ok {
		t.Error("expected config_used in response")
	}
	if _, ok := resp["agent_profile"]; !ok {
		t.Error("expected agent_profile in response")
	}

	// Test 2: Replay with custom alpha override
	body = fmt.Sprintf(`{"agent_id": %d, "ranker_params": {"alpha": 0.9}}`, userID)
	resp2 := testutil.DoPostRaw(t, "http://localhost:8092/api/v1/replay/sort", body, "")
	if resp2 == nil {
		t.Fatal("replay request with alpha override failed")
	}
	configUsed := resp2["config_used"].(map[string]interface{})
	rankerParams := configUsed["ranker_params"].(map[string]interface{})
	if alpha := rankerParams["alpha"].(float64); alpha != 0.9 {
		t.Errorf("expected alpha=0.9 in config_used, got %f", alpha)
	}

	// Test 3: Replay with inline agent profile (no DB lookup)
	body = fmt.Sprintf(`{
		"agent_id": %d,
		"agent_profile": {
			"keywords": ["blockchain", "crypto"],
			"domains": ["finance"]
		}
	}`, userID)
	resp3 := testutil.DoPostRaw(t, "http://localhost:8092/api/v1/replay/sort", body, "")
	if resp3 == nil {
		t.Fatal("replay request with inline profile failed")
	}
	agentProfile := resp3["agent_profile"].(map[string]interface{})
	profileKeywords := agentProfile["keywords"].([]interface{})
	if len(profileKeywords) != 2 || profileKeywords[0] != "blockchain" {
		t.Errorf("expected inline keywords [blockchain, crypto], got %v", profileKeywords)
	}

	// Test 4: Health check
	healthResp := testutil.DoGetRaw(t, "http://localhost:8092/health", "")
	if healthResp == nil {
		t.Fatal("health check failed")
	}
	if healthResp["status"] != "ok" {
		t.Errorf("expected status ok, got %v", healthResp["status"])
	}
}
```

Note: `DoPostRaw` and `DoGetRaw` helpers may need to be added to `testutil` if they don't exist — they are like `DoPost`/`DoGet` but target an arbitrary URL (not the default API gateway). If the existing helpers already accept a base URL parameter, use those instead. Check the testutil package and adapt the test accordingly.

- [ ] **Step 3: Build and start replay service, then run tests**

Run:
```bash
cd /Users/misaki/go/eigenflux
bash replay/scripts/build.sh
bash replay/scripts/start.sh
sleep 2
go test -v -count=1 ./tests/replay/
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add tests/replay/
git commit -m "test(replay): add integration tests for offline replay service

Tests basic replay, param overrides, inline profile, and health check."
```

---

### Task 8: Update documentation

**Files:**
- Modify: `docs/dev/configuration.md`
- Modify: `docs/dev/testing.md`

- [ ] **Step 1: Add replay port to `docs/dev/configuration.md`**

Add to the Hertz HTTP services port table:

```
| Replay    | `REPLAY_PORT`      | 8092   |
```

- [ ] **Step 2: Add replay to `docs/dev/testing.md`**

Add a section for the replay service describing:
- What it does (offline sort simulation with custom params)
- How to build and start: `bash replay/scripts/build.sh && bash replay/scripts/start.sh`
- How to run tests: `go test -v ./tests/replay/`
- Example curl: `curl -X POST http://localhost:8092/api/v1/replay/sort -d '{"agent_id": 12345}'`

- [ ] **Step 3: Commit**

```bash
git add docs/dev/configuration.md docs/dev/testing.md
git commit -m "docs: add replay service to configuration and testing docs"
```
