package dal

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const milestoneNotifyKeyPrefix = "milestone:notify:"

// MilestoneNotification is the shape stored in milestone:notify:{agent_id} Redis hash.
type MilestoneNotification struct {
	NotificationID string `json:"notification_id"`
	Type           string `json:"type"`
	Content        string `json:"content"`
	CreatedAt      int64  `json:"created_at"`
}

func milestoneNotifyKey(agentID int64) string {
	return fmt.Sprintf("%s%d", milestoneNotifyKeyPrefix, agentID)
}

// ListMilestoneNotifications reads the milestone:notify:{agentID} Redis hash.
func ListMilestoneNotifications(ctx context.Context, rdb *redis.Client, agentID int64) ([]MilestoneNotification, error) {
	values, err := rdb.HVals(ctx, milestoneNotifyKey(agentID)).Result()
	if err != nil {
		return nil, fmt.Errorf("read milestone notifications from redis: %w", err)
	}
	if len(values) == 0 {
		return nil, nil
	}

	notifications := make([]MilestoneNotification, 0, len(values))
	for _, value := range values {
		var n MilestoneNotification
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

// DeleteMilestoneNotifications removes entries from the milestone:notify:{agentID} Redis hash.
func DeleteMilestoneNotifications(ctx context.Context, rdb *redis.Client, agentID int64, eventIDs []int64) error {
	if len(eventIDs) == 0 {
		return nil
	}
	fields := make([]string, len(eventIDs))
	for i, id := range eventIDs {
		fields[i] = strconv.FormatInt(id, 10)
	}
	return rdb.HDel(ctx, milestoneNotifyKey(agentID), fields...).Err()
}

const milestoneNotificationStatusNotified int16 = 1

// MarkMilestoneEventsNotified updates milestone_events notification_status in the DB.
func MarkMilestoneEventsNotified(ctx context.Context, db *gorm.DB, eventIDs []int64, notifiedAt int64) error {
	if len(eventIDs) == 0 {
		return nil
	}
	return db.WithContext(ctx).
		Table("milestone_events").
		Where("event_id IN ?", eventIDs).
		Updates(map[string]interface{}{
			"notification_status": milestoneNotificationStatusNotified,
			"notified_at":         notifiedAt,
		}).Error
}
