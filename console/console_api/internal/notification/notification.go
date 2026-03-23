package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"console.eigenflux.ai/internal/model"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	activeSystemKey = "notify:system:active"
)

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

// ActiveStore manages the notify:system:active Redis hash.
type ActiveStore struct {
	rdb *redis.Client
}

func NewActiveStore(rdb *redis.Client) *ActiveStore {
	return &ActiveStore{rdb: rdb}
}

func (s *ActiveStore) Put(ctx context.Context, n *model.SystemNotification) error {
	data, err := json.Marshal(activePayload{
		NotificationID: n.NotificationID,
		Type:           n.Type,
		Content:        n.Content,
		Status:         n.Status,
		AudienceType:   n.AudienceType,
		StartAt:        n.StartAt,
		EndAt:          n.EndAt,
		OfflineAt:      n.OfflineAt,
		CreatedAt:      n.CreatedAt,
	})
	if err != nil {
		return fmt.Errorf("marshal active notification: %w", err)
	}
	return s.rdb.HSet(ctx, activeSystemKey, fmt.Sprintf("%d", n.NotificationID), data).Err()
}

func (s *ActiveStore) Remove(ctx context.Context, notificationID int64) error {
	return s.rdb.HDel(ctx, activeSystemKey, fmt.Sprintf("%d", notificationID)).Err()
}

func (s *ActiveStore) ReplaceAll(ctx context.Context, notifications []model.SystemNotification) error {
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, activeSystemKey)
	for i := range notifications {
		data, err := json.Marshal(activePayload{
			NotificationID: notifications[i].NotificationID,
			Type:           notifications[i].Type,
			Content:        notifications[i].Content,
			Status:         notifications[i].Status,
			AudienceType:   notifications[i].AudienceType,
			StartAt:        notifications[i].StartAt,
			EndAt:          notifications[i].EndAt,
			OfflineAt:      notifications[i].OfflineAt,
			CreatedAt:      notifications[i].CreatedAt,
		})
		if err != nil {
			continue
		}
		pipe.HSet(ctx, activeSystemKey, fmt.Sprintf("%d", notifications[i].NotificationID), data)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// Service provides notification management for the console.
type Service struct {
	db          *gorm.DB
	activeStore *ActiveStore
}

func NewService(db *gorm.DB, rdb *redis.Client) *Service {
	return &Service{
		db:          db,
		activeStore: NewActiveStore(rdb),
	}
}

func (s *Service) ActiveStore() *ActiveStore {
	return s.activeStore
}

func (s *Service) RecoverActiveNotifications(ctx context.Context) error {
	var notifications []model.SystemNotification
	err := s.db.WithContext(ctx).
		Where("status = ? AND offline_at = 0", model.StatusActive).
		Find(&notifications).Error
	if err != nil {
		return err
	}
	log.Printf("[notification] Recovered %d active system notifications to Redis", len(notifications))
	return s.activeStore.ReplaceAll(ctx, notifications)
}

// NotificationDelivery is used by tests to verify delivery state.
type NotificationDelivery struct {
	DeliveryID  int64  `gorm:"column:delivery_id;primaryKey;autoIncrement"`
	SourceType  string `gorm:"column:source_type;type:varchar(32);not null"`
	SourceID    int64  `gorm:"column:source_id;not null"`
	AgentID     int64  `gorm:"column:agent_id;not null"`
	DeliveredAt int64  `gorm:"column:delivered_at;not null"`
}

func (NotificationDelivery) TableName() string { return "notification_deliveries" }
