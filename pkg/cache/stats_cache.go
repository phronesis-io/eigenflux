package cache

import (
	"context"
	"eigenflux_server/pkg/cache/keys"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// StatsCache handles caching of item statistics
type StatsCache struct {
	cache Cache
	ttl   time.Duration
}

// NewStatsCache creates a new stats cache
func NewStatsCache(client *redis.Client, ttl time.Duration) *StatsCache {
	return &StatsCache{
		cache: NewRedisCache(client),
		ttl:   ttl,
	}
}

// CachedItemStats represents cached item statistics
type CachedItemStats struct {
	ItemID         int64 `json:"item_id"`
	AuthorAgentID  int64 `json:"author_agent_id"`
	ConsumedCount  int64 `json:"consumed_count"`
	ScoreNeg1Count int64 `json:"score_neg1_count"`
	Score0Count    int64 `json:"score_0_count"`
	Score1Count    int64 `json:"score_1_count"`
	Score2Count    int64 `json:"score_2_count"`
	TotalScore     int64 `json:"total_score"`
	UpdatedAt      int64 `json:"updated_at"`
}

// CachedInfluenceMetrics represents cached agent influence metrics
type CachedInfluenceMetrics struct {
	TotalItems    int64 `json:"total_items"`
	TotalConsumed int64 `json:"total_consumed"`
	TotalScored1  int64 `json:"total_scored_1"`
	TotalScored2  int64 `json:"total_scored_2"`
}

// BuildItemStatsKey generates a cache key for item stats
// Format: cache:item_stats:{item_id}
func (sc *StatsCache) BuildItemStatsKey(itemID int64) string {
	return fmt.Sprintf(keys.ItemStats, itemID)
}

// BuildInfluenceKey generates a cache key for agent influence metrics
// Format: cache:agent_influence:{agent_id}
func (sc *StatsCache) BuildInfluenceKey(agentID int64) string {
	return fmt.Sprintf(keys.AgentInfluence, agentID)
}

// GetItemStats retrieves cached item stats
func (sc *StatsCache) GetItemStats(ctx context.Context, itemID int64) (*CachedItemStats, error) {
	key := sc.BuildItemStatsKey(itemID)
	var stats CachedItemStats
	if err := sc.cache.Get(ctx, key, &stats); err != nil {
		return nil, err
	}
	return &stats, nil
}

// SetItemStats stores item stats in cache
func (sc *StatsCache) SetItemStats(ctx context.Context, stats *CachedItemStats) error {
	key := sc.BuildItemStatsKey(stats.ItemID)
	return sc.cache.Set(ctx, key, stats, sc.ttl)
}

// DeleteItemStats removes item stats from cache
func (sc *StatsCache) DeleteItemStats(ctx context.Context, itemID int64) error {
	key := sc.BuildItemStatsKey(itemID)
	return sc.cache.Delete(ctx, key)
}

// GetInfluence retrieves cached influence metrics
func (sc *StatsCache) GetInfluence(ctx context.Context, agentID int64) (*CachedInfluenceMetrics, error) {
	key := sc.BuildInfluenceKey(agentID)
	var metrics CachedInfluenceMetrics
	if err := sc.cache.Get(ctx, key, &metrics); err != nil {
		return nil, err
	}
	return &metrics, nil
}

// SetInfluence stores influence metrics in cache
func (sc *StatsCache) SetInfluence(ctx context.Context, agentID int64, metrics *CachedInfluenceMetrics) error {
	key := sc.BuildInfluenceKey(agentID)
	return sc.cache.Set(ctx, key, metrics, sc.ttl)
}

// DeleteInfluence removes influence metrics from cache
func (sc *StatsCache) DeleteInfluence(ctx context.Context, agentID int64) error {
	key := sc.BuildInfluenceKey(agentID)
	return sc.cache.Delete(ctx, key)
}
