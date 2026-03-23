package main

import (
	"context"
	"log"
	"sort"
	"strconv"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/base"
	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
	"eigenflux_server/rpc/notification/dal"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type NotificationServiceImpl struct {
	db          *gorm.DB
	rdb         *redis.Client
	activeStore *dal.ActiveStore
}

func NewNotificationServiceImpl(db *gorm.DB, rdb *redis.Client) *NotificationServiceImpl {
	return &NotificationServiceImpl{
		db:          db,
		rdb:         rdb,
		activeStore: dal.NewActiveStore(rdb),
	}
}

func (s *NotificationServiceImpl) ListPending(ctx context.Context, req *notificationrpc.ListPendingReq) (*notificationrpc.ListPendingResp, error) {
	if req == nil {
		return &notificationrpc.ListPendingResp{
			Notifications: []*notificationrpc.PendingNotification{},
			BaseResp:      &base.BaseResp{Code: 400, Msg: "nil request"},
		}, nil
	}

	var all []*notificationrpc.PendingNotification

	// 1. Milestone notifications from Redis
	milestoneNotifs, err := dal.ListMilestoneNotifications(ctx, s.rdb, req.AgentId)
	if err != nil {
		log.Printf("[Notification] Failed to list milestone notifications for agent %d: %v", req.AgentId, err)
	} else {
		for _, n := range milestoneNotifs {
			eventID, err := strconv.ParseInt(n.NotificationID, 10, 64)
			if err != nil {
				log.Printf("[Notification] Invalid milestone notification id %q: %v", n.NotificationID, err)
				continue
			}
			all = append(all, &notificationrpc.PendingNotification{
				NotificationId: eventID,
				SourceType:     dal.SourceTypeMilestone,
				Type:           n.Type,
				Content:        n.Content,
				CreatedAt:      n.CreatedAt,
			})
		}
	}

	// 2. System notifications from Redis active store + DB delivery check
	sysNotifs, err := s.listPendingSystemNotifications(ctx, req.AgentId)
	if err != nil {
		log.Printf("[Notification] Failed to list system notifications for agent %d: %v", req.AgentId, err)
	} else {
		for _, n := range sysNotifs {
			all = append(all, &notificationrpc.PendingNotification{
				NotificationId: n.NotificationID,
				SourceType:     dal.SourceTypeSystem,
				Type:           n.Type,
				Content:        n.Content,
				CreatedAt:      n.CreatedAt,
			})
		}
	}

	// Sort by created_at ASC, notification_id ASC
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt != all[j].CreatedAt {
			return all[i].CreatedAt < all[j].CreatedAt
		}
		return all[i].NotificationId < all[j].NotificationId
	})

	if all == nil {
		all = []*notificationrpc.PendingNotification{}
	}

	return &notificationrpc.ListPendingResp{
		Notifications: all,
		BaseResp:      &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *NotificationServiceImpl) AckNotifications(ctx context.Context, req *notificationrpc.AckNotificationsReq) (*notificationrpc.AckNotificationsResp, error) {
	if req == nil {
		return &notificationrpc.AckNotificationsResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "nil request"},
		}, nil
	}
	if len(req.Items) == 0 {
		return &notificationrpc.AckNotificationsResp{
			BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
		}, nil
	}

	var milestoneIDs []int64
	var systemItems []dal.NotificationDelivery
	now := time.Now().UnixMilli()

	for _, item := range req.Items {
		if item == nil {
			continue
		}
		switch item.SourceType {
		case dal.SourceTypeMilestone:
			milestoneIDs = append(milestoneIDs, item.NotificationId)
		case dal.SourceTypeSystem:
			systemItems = append(systemItems, dal.NotificationDelivery{
				SourceType:  dal.SourceTypeSystem,
				SourceID:    item.NotificationId,
				AgentID:     req.AgentId,
				DeliveredAt: now,
			})
		default:
			log.Printf("[Notification] Unknown source_type in ack: %s", item.SourceType)
		}
	}

	// Ack milestone: delete from Redis + mark notified in DB
	if len(milestoneIDs) > 0 {
		if err := dal.DeleteMilestoneNotifications(ctx, s.rdb, req.AgentId, milestoneIDs); err != nil {
			log.Printf("[Notification] Failed to delete milestone notifications from Redis for agent %d: %v", req.AgentId, err)
		}
		if err := dal.MarkMilestoneEventsNotified(ctx, s.db, milestoneIDs, now); err != nil {
			log.Printf("[Notification] Failed to mark milestone events notified for agent %d: %v", req.AgentId, err)
		}
	}

	// Ack system: insert delivery records
	if len(systemItems) > 0 {
		if err := dal.RecordDeliveries(ctx, s.db, systemItems); err != nil {
			log.Printf("[Notification] Failed to record system notification deliveries for agent %d: %v", req.AgentId, err)
		}
	}

	return &notificationrpc.AckNotificationsResp{
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

// listPendingSystemNotifications returns system notifications not yet delivered to the agent.
func (s *NotificationServiceImpl) listPendingSystemNotifications(ctx context.Context, agentID int64) ([]pendingSystem, error) {
	active, err := s.activeStore.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}

	nowMS := time.Now().UnixMilli()

	var candidates []dal.SystemNotification
	for i := range active {
		if active[i].IsActive(nowMS) {
			candidates = append(candidates, active[i])
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	sourceIDs := make([]int64, len(candidates))
	for i, c := range candidates {
		sourceIDs[i] = c.NotificationID
	}
	delivered, err := dal.AreDelivered(ctx, s.db, dal.SourceTypeSystem, sourceIDs, agentID)
	if err != nil {
		return nil, err
	}

	var pending []pendingSystem
	for _, c := range candidates {
		if delivered[c.NotificationID] {
			continue
		}
		pending = append(pending, pendingSystem{
			NotificationID: c.NotificationID,
			Type:           c.Type,
			Content:        c.Content,
			CreatedAt:      c.CreatedAt,
		})
	}

	sort.Slice(pending, func(i, j int) bool {
		if pending[i].CreatedAt != pending[j].CreatedAt {
			return pending[i].CreatedAt < pending[j].CreatedAt
		}
		return pending[i].NotificationID < pending[j].NotificationID
	})

	return pending, nil
}

type pendingSystem struct {
	NotificationID int64
	Type           string
	Content        string
	CreatedAt      int64
}

// RecoverActiveNotifications rebuilds the notify:system:active Redis key from the DB.
func (s *NotificationServiceImpl) RecoverActiveNotifications(ctx context.Context) error {
	var notifications []dal.SystemNotification
	err := s.db.WithContext(ctx).
		Where("status = ?", dal.StatusActive).
		Where("offline_at = 0").
		Find(&notifications).Error
	if err != nil {
		return err
	}
	log.Printf("[Notification] Recovered %d active system notifications to Redis", len(notifications))
	return s.activeStore.ReplaceAll(ctx, notifications)
}
