package dal

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/redis/go-redis/v9"
)

const pmNotifyKeyPrefix = "pm:notify:"

// PMNotification is the shape stored in pm:notify:{agent_id} Redis hash.
type PMNotification struct {
	NotificationID string `json:"notification_id"`
	Type           string `json:"type"`
	Content        string `json:"content"`
	CreatedAt      int64  `json:"created_at"`
}

func pmNotifyKey(agentID int64) string {
	return fmt.Sprintf("%s%d", pmNotifyKeyPrefix, agentID)
}

// ListPMNotifications reads the pm:notify:{agentID} Redis hash.
func ListPMNotifications(ctx context.Context, rdb *redis.Client, agentID int64) ([]PMNotification, error) {
	values, err := rdb.HVals(ctx, pmNotifyKey(agentID)).Result()
	if err != nil {
		return nil, fmt.Errorf("read pm notifications from redis: %w", err)
	}
	if len(values) == 0 {
		return nil, nil
	}

	notifications := make([]PMNotification, 0, len(values))
	for _, value := range values {
		var n PMNotification
		if err := json.Unmarshal([]byte(value), &n); err != nil {
			continue
		}
		notifications = append(notifications, n)
	}

	sort.Slice(notifications, func(i, j int) bool {
		if notifications[i].CreatedAt != notifications[j].CreatedAt {
			return notifications[i].CreatedAt < notifications[j].CreatedAt
		}
		left, lErr := strconv.ParseInt(notifications[i].NotificationID, 10, 64)
		right, rErr := strconv.ParseInt(notifications[j].NotificationID, 10, 64)
		if lErr == nil && rErr == nil {
			return left < right
		}
		return notifications[i].NotificationID < notifications[j].NotificationID
	})
	return notifications, nil
}

// DeletePMNotifications removes entries from the pm:notify:{agentID} Redis hash.
func DeletePMNotifications(ctx context.Context, rdb *redis.Client, agentID int64, requestIDs []int64) error {
	if len(requestIDs) == 0 {
		return nil
	}
	fields := make([]string, len(requestIDs))
	for i, id := range requestIDs {
		fields[i] = strconv.FormatInt(id, 10)
	}
	return rdb.HDel(ctx, pmNotifyKey(agentID), fields...).Err()
}
