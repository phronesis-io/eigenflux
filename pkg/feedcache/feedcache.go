package feedcache

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// FeedCacheKeyPrefix is the prefix for feed cache keys
	FeedCacheKeyPrefix = "feed:cache:"
	// FeedCacheTTL is the TTL for feed cache (30 minutes)
	FeedCacheTTL = 30 * time.Minute
)

// FeedCache provides user-specific feed caching
type FeedCache struct {
	rdb *redis.Client
}

// Entry represents one cached feed item plus optional replay metadata
// needed when the item is eventually served via load_more.
type Entry struct {
	GroupID       int64   `json:"group_id"`
	ItemID        int64   `json:"item_id,omitempty"`
	Position      int     `json:"position,omitempty"`
	ImpressionID  string  `json:"impression_id,omitempty"`
	Score         float64 `json:"score"`
	AgentFeatures string  `json:"agent_features,omitempty"`
	ItemFeatures  string  `json:"item_features,omitempty"`
}

// NewFeedCache creates a new FeedCache instance
func NewFeedCache(rdb *redis.Client) *FeedCache {
	return &FeedCache{rdb: rdb}
}

// GetKey returns the cache key for a specific agent
func GetKey(agentID int64) string {
	return fmt.Sprintf("%s%d", FeedCacheKeyPrefix, agentID)
}

// Clear clears the cache for a specific agent
func (fc *FeedCache) Clear(ctx context.Context, agentID int64) error {
	key := GetKey(agentID)
	return fc.rdb.Del(ctx, key).Err()
}

// Push pushes cached entries to the end of the cache list.
func (fc *FeedCache) Push(ctx context.Context, agentID int64, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}

	key := GetKey(agentID)
	values := make([]interface{}, 0, len(entries))
	for _, entry := range entries {
		payload, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		values = append(values, string(payload))
	}

	pipe := fc.rdb.Pipeline()
	pipe.RPush(ctx, key, values...)
	pipe.Expire(ctx, key, FeedCacheTTL)
	_, err := pipe.Exec(ctx)
	return err
}

// Pop pops up to limit cached entries from the front of the cache list.
func (fc *FeedCache) Pop(ctx context.Context, agentID int64, limit int) ([]Entry, error) {
	key := GetKey(agentID)

	// Use LPOP with count (Redis 6.2+)
	result, err := fc.rdb.LPopCount(ctx, key, limit).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}

	entries := make([]Entry, 0, len(result))
	for _, s := range result {
		entry, ok := parseEntry(s)
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// Len returns the length of the cache list
func (fc *FeedCache) Len(ctx context.Context, agentID int64) (int64, error) {
	key := GetKey(agentID)
	return fc.rdb.LLen(ctx, key).Result()
}

func parseEntry(raw string) (Entry, bool) {
	var entry Entry
	if err := json.Unmarshal([]byte(raw), &entry); err == nil && entry.GroupID != 0 {
		return entry, true
	}

	groupID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || groupID == 0 {
		return Entry{}, false
	}
	return Entry{GroupID: groupID}, true
}
