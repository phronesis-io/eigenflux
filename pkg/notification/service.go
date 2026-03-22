package notification

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Service provides notification aggregation for the feed layer.
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

// ActiveStore exposes the underlying store for console write operations.
func (s *Service) ActiveStore() *ActiveStore {
	return s.activeStore
}

// ListPendingSystemNotifications returns system notifications that should be
// delivered to the given agent on this refresh.
func (s *Service) ListPendingSystemNotifications(ctx context.Context, agentID int64) ([]PendingNotification, error) {
	active, err := s.activeStore.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}

	nowMS := time.Now().UnixMilli()

	// Filter by lifecycle
	var candidates []SystemNotification
	for i := range active {
		if active[i].IsActive(nowMS) {
			candidates = append(candidates, active[i])
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Batch check delivery state
	sourceIDs := make([]int64, len(candidates))
	for i, c := range candidates {
		sourceIDs[i] = c.NotificationID
	}
	delivered, err := AreDelivered(ctx, s.db, SourceTypeSystem, sourceIDs, agentID)
	if err != nil {
		return nil, err
	}

	var pending []PendingNotification
	for _, c := range candidates {
		if delivered[c.NotificationID] {
			continue
		}
		pending = append(pending, PendingNotification{
			NotificationID: c.NotificationID,
			SourceType:     SourceTypeSystem,
			Type:           c.Type,
			Content:        c.Content,
			CreatedAt:      c.CreatedAt,
		})
	}

	// Sort by created_at ASC, notification_id ASC
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].CreatedAt != pending[j].CreatedAt {
			return pending[i].CreatedAt < pending[j].CreatedAt
		}
		return pending[i].NotificationID < pending[j].NotificationID
	})

	return pending, nil
}

// AckNotifications records deliveries for the given notification items.
func (s *Service) AckNotifications(ctx context.Context, agentID int64, items []AckItem) error {
	if len(items) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	rows := make([]NotificationDelivery, len(items))
	for i, item := range items {
		rows[i] = NotificationDelivery{
			SourceType:  item.SourceType,
			SourceID:    item.SourceID,
			AgentID:     agentID,
			DeliveredAt: now,
		}
	}
	return RecordDeliveries(ctx, s.db, rows)
}

// AckItem represents one notification to acknowledge.
type AckItem struct {
	SourceType string
	SourceID   int64
}

// RecoverActiveNotifications rebuilds the notify:system:active Redis key from the DB.
func (s *Service) RecoverActiveNotifications(ctx context.Context) error {
	var notifications []SystemNotification
	err := s.db.WithContext(ctx).
		Where("status = ?", StatusActive).
		Where("offline_at = 0").
		Find(&notifications).Error
	if err != nil {
		return err
	}
	log.Printf("[notification] Recovered %d active system notifications to Redis", len(notifications))
	return s.activeStore.ReplaceAll(ctx, notifications)
}
