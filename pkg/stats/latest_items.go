package stats

import (
	"context"
	"eigenflux_server/pkg/json"
	"eigenflux_server/pkg/logger"
	"fmt"
	"sort"
	"strings"

	"github.com/redis/go-redis/v9"
)

const (
	// Redis key for latest items list
	KeyLatestItems = "public:latest_items"
	// Redis key that tracks the active latest item type buckets
	KeyLatestItemTypes = "public:latest_items:types"
	// Redis key prefix for per-type latest item lists
	KeyLatestItemsTypePrefix = "public:latest_items:type:"
	// Maximum number of items to keep in the list
	MaxLatestItems = 50
)

var latestItemTypePriority = []string{"alert", "demand", "supply", "info"}

// ItemSnapshot represents a snapshot of an item for the latest items list
type ItemSnapshot struct {
	ID      int64             `json:"id"`
	Agent   string            `json:"agent"`
	Country string            `json:"country"`
	Type    string            `json:"type"`
	Domains []string          `json:"domains"`
	Content string            `json:"content"`
	URL     string            `json:"url"`
	Notes   map[string]string `json:"notes"`
}

// PushLatestItem pushes an item to the latest items list and trims to max size
func PushLatestItem(ctx context.Context, rdb *redis.Client, item *ItemSnapshot) error {
	item.Type = normalizeLatestItemType(item.Type)

	// Serialize item to JSON
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("failed to marshal item: %w", err)
	}

	typeKey := latestItemsTypeKey(item.Type)
	pipe := rdb.TxPipeline()
	pipe.SAdd(ctx, KeyLatestItemTypes, item.Type)
	pipe.LPush(ctx, typeKey, string(data))
	pipe.LTrim(ctx, typeKey, 0, MaxLatestItems-1)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to push item to type list: %w", err)
	}

	if err := rebuildLatestItemsList(ctx, rdb); err != nil {
		return fmt.Errorf("failed to rebuild latest items list: %w", err)
	}

	return nil
}

// GetLatestItems retrieves the latest N items from the list
func GetLatestItems(ctx context.Context, rdb *redis.Client, limit int) ([]*ItemSnapshot, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > MaxLatestItems {
		limit = MaxLatestItems
	}

	// Get items from list (LRANGE 0 to limit-1)
	items, err := rdb.LRange(ctx, KeyLatestItems, 0, int64(limit-1)).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get latest items: %w", err)
	}

	// Deserialize items
	result := make([]*ItemSnapshot, 0, len(items))
	for _, itemStr := range items {
		var item ItemSnapshot
		if err := json.Unmarshal([]byte(itemStr), &item); err != nil {
			logger.Default().Warn("failed to unmarshal item", "err", err)
			continue
		}
		result = append(result, &item)
	}

	return result, nil
}

// ClearLatestItems clears the latest items list (for testing)
func ClearLatestItems(ctx context.Context, rdb *redis.Client) error {
	types, err := rdb.SMembers(ctx, KeyLatestItemTypes).Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("failed to list latest item types: %w", err)
	}

	keys := make([]string, 0, len(types)+2)
	keys = append(keys, KeyLatestItems, KeyLatestItemTypes)
	for _, itemType := range types {
		keys = append(keys, latestItemsTypeKey(itemType))
	}

	return rdb.Del(ctx, keys...).Err()
}

func rebuildLatestItemsList(ctx context.Context, rdb *redis.Client) error {
	itemTypes, err := rdb.SMembers(ctx, KeyLatestItemTypes).Result()
	if err != nil {
		return fmt.Errorf("failed to get latest item types: %w", err)
	}

	if len(itemTypes) == 0 {
		return rdb.Del(ctx, KeyLatestItems).Err()
	}

	orderedTypes := orderLatestItemTypes(itemTypes)
	pipe := rdb.Pipeline()
	typeCmds := make(map[string]*redis.StringSliceCmd, len(orderedTypes))
	for _, itemType := range orderedTypes {
		typeCmds[itemType] = pipe.LRange(ctx, latestItemsTypeKey(itemType), 0, MaxLatestItems-1)
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return fmt.Errorf("failed to load latest item type buckets: %w", err)
	}

	itemsByType := make(map[string][]*ItemSnapshot, len(orderedTypes))
	for _, itemType := range orderedTypes {
		rawItems, err := typeCmds[itemType].Result()
		if err != nil && err != redis.Nil {
			return fmt.Errorf("failed to get items for type %s: %w", itemType, err)
		}

		bucket := make([]*ItemSnapshot, 0, len(rawItems))
		for _, itemStr := range rawItems {
			var item ItemSnapshot
			if err := json.Unmarshal([]byte(itemStr), &item); err != nil {
				logger.Default().Warn("failed to unmarshal latest item bucket entry", "type", itemType, "err", err)
				continue
			}
			bucket = append(bucket, &item)
		}
		itemsByType[itemType] = bucket
	}

	merged := interleaveLatestItems(itemsByType, orderedTypes, MaxLatestItems)
	writePipe := rdb.TxPipeline()
	writePipe.Del(ctx, KeyLatestItems)
	if len(merged) > 0 {
		values := make([]interface{}, 0, len(merged))
		for _, item := range merged {
			data, err := json.Marshal(item)
			if err != nil {
				return fmt.Errorf("failed to marshal merged latest item: %w", err)
			}
			values = append(values, string(data))
		}
		writePipe.RPush(ctx, KeyLatestItems, values...)
	}
	if _, err := writePipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to update latest items list: %w", err)
	}

	return nil
}

func interleaveLatestItems(itemsByType map[string][]*ItemSnapshot, orderedTypes []string, limit int) []*ItemSnapshot {
	result := make([]*ItemSnapshot, 0, limit)
	indexes := make(map[string]int, len(orderedTypes))
	seen := make(map[int64]struct{}, limit)

	for len(result) < limit {
		progressed := false
		for _, itemType := range orderedTypes {
			items := itemsByType[itemType]
			idx := indexes[itemType]
			for idx < len(items) {
				item := items[idx]
				idx++
				if _, exists := seen[item.ID]; exists {
					continue
				}
				seen[item.ID] = struct{}{}
				result = append(result, item)
				progressed = true
				break
			}
			indexes[itemType] = idx

			if len(result) >= limit {
				break
			}
		}

		if !progressed {
			break
		}
	}

	return result
}

func orderLatestItemTypes(itemTypes []string) []string {
	typeSet := make(map[string]struct{}, len(itemTypes))
	for _, itemType := range itemTypes {
		if normalized := normalizeLatestItemType(itemType); normalized != "" {
			typeSet[normalized] = struct{}{}
		}
	}

	ordered := make([]string, 0, len(typeSet))
	for _, itemType := range latestItemTypePriority {
		if _, ok := typeSet[itemType]; ok {
			ordered = append(ordered, itemType)
			delete(typeSet, itemType)
		}
	}

	remaining := make([]string, 0, len(typeSet))
	for itemType := range typeSet {
		remaining = append(remaining, itemType)
	}
	sort.Strings(remaining)

	return append(ordered, remaining...)
}

func latestItemsTypeKey(itemType string) string {
	return KeyLatestItemsTypePrefix + normalizeLatestItemType(itemType)
}

func normalizeLatestItemType(itemType string) string {
	normalized := strings.ToLower(strings.TrimSpace(itemType))
	if normalized == "" {
		return "info"
	}
	return normalized
}
