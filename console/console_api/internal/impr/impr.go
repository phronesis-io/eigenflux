package impr

import (
	"context"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
)

const (
	KeyItemIDs  = "impr:agent:%d:items"
	KeyGroupIDs = "impr:agent:%d:groups"
	KeyURLs     = "impr:agent:%d:urls"
)

type SeenItems struct {
	ItemIDs  []int64
	GroupIDs []int64
	URLs     []string
}

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
