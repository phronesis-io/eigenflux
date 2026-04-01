package stats

import (
	"context"
	"eigenflux_server/pkg/logger"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

const (
	// Redis key for latest items list
	KeyLatestItems = "public:latest_items"
	// Maximum number of items to keep in the list
	MaxLatestItems = 50
)

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
	// Serialize item to JSON
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("failed to marshal item: %w", err)
	}

	// Push to list (LPUSH adds to the head)
	if err := rdb.LPush(ctx, KeyLatestItems, string(data)).Err(); err != nil {
		return fmt.Errorf("failed to push item to list: %w", err)
	}

	// Trim list to max size (keep only the first MaxLatestItems)
	if err := rdb.LTrim(ctx, KeyLatestItems, 0, MaxLatestItems-1).Err(); err != nil {
		logger.Default().Warn("failed to trim latest items list", "err", err)
		// Don't return error, trimming is best-effort
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
	return rdb.Del(ctx, KeyLatestItems).Err()
}
