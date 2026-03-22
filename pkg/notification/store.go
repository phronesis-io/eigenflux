package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	activeSystemKey    = "notify:system:active"
	activeSystemTTL    = 7 * 24 * time.Hour
	pendingKeyPrefix   = "notify:pending:"
	pendingKeyTTL      = 7 * 24 * time.Hour
)

// ActiveStore manages the `notify:system:active` Redis hash.
type ActiveStore struct {
	rdb *redis.Client
}

func NewActiveStore(rdb *redis.Client) *ActiveStore {
	return &ActiveStore{rdb: rdb}
}

type activePayload struct {
	NotificationID int64  `json:"notification_id"`
	Type           string `json:"type"`
	Content        string `json:"content"`
	Status         int16  `json:"status"`
	AudienceType   string `json:"audience_type"`
	StartAt        int64  `json:"start_at"`
	EndAt          int64  `json:"end_at"`
	OfflineAt      int64  `json:"offline_at"`
	CreatedAt      int64  `json:"created_at"`
}

func payloadFromNotification(n *SystemNotification) activePayload {
	return activePayload{
		NotificationID: n.NotificationID,
		Type:           n.Type,
		Content:        n.Content,
		Status:         n.Status,
		AudienceType:   n.AudienceType,
		StartAt:        n.StartAt,
		EndAt:          n.EndAt,
		OfflineAt:      n.OfflineAt,
		CreatedAt:      n.CreatedAt,
	}
}

// Put writes or updates one system notification in the active set.
func (s *ActiveStore) Put(ctx context.Context, n *SystemNotification) error {
	data, err := json.Marshal(payloadFromNotification(n))
	if err != nil {
		return fmt.Errorf("marshal active notification: %w", err)
	}
	field := fmt.Sprintf("%d", n.NotificationID)
	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, activeSystemKey, field, data)
	pipe.Expire(ctx, activeSystemKey, activeSystemTTL)
	_, err = pipe.Exec(ctx)
	return err
}

// Remove deletes one notification from the active set.
func (s *ActiveStore) Remove(ctx context.Context, notificationID int64) error {
	return s.rdb.HDel(ctx, activeSystemKey, fmt.Sprintf("%d", notificationID)).Err()
}

// List returns all entries in notify:system:active.
func (s *ActiveStore) List(ctx context.Context) ([]SystemNotification, error) {
	vals, err := s.rdb.HVals(ctx, activeSystemKey).Result()
	if err != nil {
		return nil, fmt.Errorf("read active system notifications: %w", err)
	}
	result := make([]SystemNotification, 0, len(vals))
	for _, raw := range vals {
		var p activePayload
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			continue
		}
		result = append(result, SystemNotification{
			NotificationID: p.NotificationID,
			Type:           p.Type,
			Content:        p.Content,
			Status:         p.Status,
			AudienceType:   p.AudienceType,
			StartAt:        p.StartAt,
			EndAt:          p.EndAt,
			OfflineAt:      p.OfflineAt,
			CreatedAt:      p.CreatedAt,
		})
	}
	return result, nil
}

// ReplaceAll replaces the entire active set with the given notifications.
func (s *ActiveStore) ReplaceAll(ctx context.Context, notifications []SystemNotification) error {
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, activeSystemKey)
	for i := range notifications {
		data, err := json.Marshal(payloadFromNotification(&notifications[i]))
		if err != nil {
			continue
		}
		pipe.HSet(ctx, activeSystemKey, fmt.Sprintf("%d", notifications[i].NotificationID), data)
	}
	if len(notifications) > 0 {
		pipe.Expire(ctx, activeSystemKey, activeSystemTTL)
	}
	_, err := pipe.Exec(ctx)
	return err
}
