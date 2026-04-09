package milestone

import (
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	milestonedal "eigenflux_server/pkg/milestone/dal"

	"github.com/redis/go-redis/v9"
)

const (
	NotificationKeyPrefix  = "milestone:notify:"
	DefaultNotificationTTL = 7 * 24 * time.Hour
)

type NotificationStore struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewNotificationStore(rdb *redis.Client, ttl time.Duration) *NotificationStore {
	if ttl <= 0 {
		ttl = DefaultNotificationTTL
	}
	return &NotificationStore{rdb: rdb, ttl: ttl}
}

func NotificationKey(agentID int64) string {
	return fmt.Sprintf("%s%d", NotificationKeyPrefix, agentID)
}

func (s *NotificationStore) Put(ctx context.Context, agentID int64, notification Notification) error {
	payload, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("marshal milestone notification: %w", err)
	}

	key := NotificationKey(agentID)
	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, key, notification.NotificationID, payload)
	pipe.Expire(ctx, key, s.ttl)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("write milestone notification to redis: %w", err)
	}
	return nil
}

func (s *NotificationStore) List(ctx context.Context, agentID int64) ([]Notification, error) {
	values, err := s.rdb.HVals(ctx, NotificationKey(agentID)).Result()
	if err != nil {
		return nil, fmt.Errorf("read milestone notifications from redis: %w", err)
	}
	if len(values) == 0 {
		return []Notification{}, nil
	}

	notifications := make([]Notification, 0, len(values))
	for _, value := range values {
		var notification Notification
		if err := json.Unmarshal([]byte(value), &notification); err != nil {
			return nil, fmt.Errorf("unmarshal milestone notification: %w", err)
		}
		notifications = append(notifications, notification)
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

func (s *NotificationStore) Delete(ctx context.Context, agentID int64, eventIDs []int64) error {
	if len(eventIDs) == 0 {
		return nil
	}
	fields := make([]string, len(eventIDs))
	for i, eventID := range eventIDs {
		fields[i] = strconv.FormatInt(eventID, 10)
	}
	if err := s.rdb.HDel(ctx, NotificationKey(agentID), fields...).Err(); err != nil {
		return fmt.Errorf("delete milestone notifications from redis: %w", err)
	}
	return nil
}

func NotificationFromEvent(event milestonedal.MilestoneEvent) Notification {
	return Notification{
		NotificationID: strconv.FormatInt(event.EventID, 10),
		Type:           NotificationTypeMilestone,
		Content:        event.NotificationContent,
		CreatedAt:      event.QueuedAt,
	}
}
