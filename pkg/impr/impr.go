package impr

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// ImprTTL is the TTL for impression records (used for feedback validation only).
	ImprTTL = 30 * 24 * time.Hour

	KeyItemIDs  = "impr:agent:%d:items"
	KeyGroupIDs = "impr:agent:%d:groups"
	KeyURLs     = "impr:agent:%d:urls"
)

// ImprItem represents an item to record as seen.
type ImprItem struct {
	ItemID  int64
	GroupID int64
	URL     string
}

// RecordImpressions writes impression records to Redis (SADD + Expire).
func RecordImpressions(ctx context.Context, rdb *redis.Client, agentID int64, items []ImprItem) error {
	if len(items) == 0 {
		return nil
	}

	itemKey := fmt.Sprintf(KeyItemIDs, agentID)
	groupKey := fmt.Sprintf(KeyGroupIDs, agentID)
	urlKey := fmt.Sprintf(KeyURLs, agentID)

	pipe := rdb.Pipeline()
	for _, item := range items {
		pipe.SAdd(ctx, itemKey, item.ItemID)
		if item.GroupID != 0 {
			pipe.SAdd(ctx, groupKey, item.GroupID)
		}
		if item.URL != "" {
			pipe.SAdd(ctx, urlKey, item.URL)
		}
	}
	// Refresh TTL
	pipe.Expire(ctx, itemKey, ImprTTL)
	pipe.Expire(ctx, groupKey, ImprTTL)
	pipe.Expire(ctx, urlKey, ImprTTL)

	_, err := pipe.Exec(ctx)
	return err
}

// SeenItems holds the sets of already-seen identifiers for an agent.
type SeenItems struct {
	ItemIDs  []int64
	GroupIDs []int64
	URLs     []string
}

// GetSeenItems reads impression records from Redis (SMEMBERS).
func GetSeenItems(ctx context.Context, rdb *redis.Client, agentID int64) (*SeenItems, error) {
	itemKey := fmt.Sprintf(KeyItemIDs, agentID)
	groupKey := fmt.Sprintf(KeyGroupIDs, agentID)
	urlKey := fmt.Sprintf(KeyURLs, agentID)

	pipe := rdb.Pipeline()
	itemCmd := pipe.SMembers(ctx, itemKey)
	groupCmd := pipe.SMembers(ctx, groupKey)
	urlCmd := pipe.SMembers(ctx, urlKey)
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}

	var itemIDs []int64
	for _, s := range itemCmd.Val() {
		id, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			itemIDs = append(itemIDs, id)
		}
	}
	if itemIDs == nil {
		itemIDs = []int64{}
	}

	var groupIDs []int64
	for _, s := range groupCmd.Val() {
		id, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			groupIDs = append(groupIDs, id)
		}
	}
	if groupIDs == nil {
		groupIDs = []int64{}
	}

	urls := urlCmd.Val()
	if urls == nil {
		urls = []string{}
	}

	return &SeenItems{
		ItemIDs:  itemIDs,
		GroupIDs: groupIDs,
		URLs:     urls,
	}, nil
}
