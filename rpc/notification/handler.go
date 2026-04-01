package main

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/base"
	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
	"eigenflux_server/pkg/audience"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/reqinfo"
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
	logger.FromContext(ctx).Debug("ListPending called", "agentID", req.GetAgentId())
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
		logger.FromContext(ctx).Error("failed to list milestone notifications", "agentID", req.AgentId, "err", err)
	} else {
		for _, n := range milestoneNotifs {
			eventID, err := strconv.ParseInt(n.NotificationID, 10, 64)
			if err != nil {
				logger.FromContext(ctx).Warn("invalid milestone notification id", "notificationID", n.NotificationID, "err", err)
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
	vars := reqinfo.ClientFromContext(ctx).ToVars()
	for k, v := range reqinfo.AuthFromContext(ctx).ToVars() {
		vars[k] = v
	}
	sysNotifs, err := s.listPendingSystemNotifications(ctx, req.AgentId, vars)
	if err != nil {
		logger.FromContext(ctx).Error("failed to list system notifications", "agentID", req.AgentId, "err", err)
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

	// 3. PM (friend request) notifications from Redis
	pmNotifs, err := dal.ListPMNotifications(ctx, s.rdb, req.AgentId)
	if err != nil {
		logger.FromContext(ctx).Error("failed to list PM notifications", "agentID", req.AgentId, "err", err)
	} else {
		for _, n := range pmNotifs {
			requestID, err := strconv.ParseInt(n.NotificationID, 10, 64)
			if err != nil {
				logger.FromContext(ctx).Warn("invalid PM notification id", "id", n.NotificationID, "err", err)
				continue
			}
			all = append(all, &notificationrpc.PendingNotification{
				NotificationId: requestID,
				SourceType:     dal.SourceTypeFriendRequest,
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
	logger.FromContext(ctx).Debug("AckNotifications called", "agentID", req.GetAgentId(), "items", len(req.GetItems()))
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
	var pmIDs []int64
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
		case dal.SourceTypeFriendRequest:
			pmIDs = append(pmIDs, item.NotificationId)
		default:
			logger.FromContext(ctx).Warn("unknown source_type in ack", "sourceType", item.SourceType)
		}
	}

	// Ack milestone: delete from Redis + mark notified in DB
	if len(milestoneIDs) > 0 {
		if err := dal.DeleteMilestoneNotifications(ctx, s.rdb, req.AgentId, milestoneIDs); err != nil {
			logger.FromContext(ctx).Error("failed to delete milestone notifications from Redis", "agentID", req.AgentId, "err", err)
		}
		if err := dal.MarkMilestoneEventsNotified(ctx, s.db, milestoneIDs, now); err != nil {
			logger.FromContext(ctx).Error("failed to mark milestone events notified", "agentID", req.AgentId, "err", err)
		}
	}

	// Ack system: insert delivery records
	if len(systemItems) > 0 {
		if err := dal.RecordDeliveries(ctx, s.db, systemItems); err != nil {
			logger.FromContext(ctx).Error("failed to record system notification deliveries", "agentID", req.AgentId, "err", err)
		}
	}

	// Ack friend request: delete from Redis
	if len(pmIDs) > 0 {
		if err := dal.DeletePMNotifications(ctx, s.rdb, req.AgentId, pmIDs); err != nil {
			logger.FromContext(ctx).Error("failed to delete PM notifications from Redis", "agentID", req.AgentId, "err", err)
		}
	}

	return &notificationrpc.AckNotificationsResp{
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

// listPendingSystemNotifications returns system notifications pending for the agent.
// type=system (persistent): always returned while IsActive, delivery check skipped.
// type=announcement (one-time): only returned if not yet delivered.
func (s *NotificationServiceImpl) listPendingSystemNotifications(ctx context.Context, agentID int64, contextVars map[string]string) ([]pendingSystem, error) {
	active, err := s.activeStore.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}

	nowMS := time.Now().UnixMilli()

	var persistent []dal.SystemNotification
	var oneTime []dal.SystemNotification
	for i := range active {
		if !active[i].IsActive(nowMS) {
			continue
		}
		if active[i].AudienceType == dal.AudienceTypeExpression && active[i].AudienceExpression != "" {
			match, err := audience.Evaluate(active[i].AudienceExpression, contextVars)
			if err != nil {
				logger.FromContext(ctx).Warn("audience expression error", "notificationID", active[i].NotificationID, "err", err)
				continue
			}
			if !match {
				continue
			}
		}
		if active[i].Type == dal.TypeSystem {
			persistent = append(persistent, active[i])
		} else {
			oneTime = append(oneTime, active[i])
		}
	}

	// Delivery check only for one-time (announcement) notifications
	var delivered map[int64]bool
	if len(oneTime) > 0 {
		sourceIDs := make([]int64, len(oneTime))
		for i, c := range oneTime {
			sourceIDs[i] = c.NotificationID
		}
		delivered, err = dal.AreDelivered(ctx, s.db, dal.SourceTypeSystem, sourceIDs, agentID)
		if err != nil {
			return nil, err
		}
	}

	var pending []pendingSystem

	// Persistent notifications: always included
	for _, c := range persistent {
		pending = append(pending, pendingSystem{
			NotificationID: c.NotificationID,
			Type:           c.Type,
			Content:        c.Content,
			CreatedAt:      c.CreatedAt,
		})
	}

	// One-time notifications: only if not yet delivered
	for _, c := range oneTime {
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
	slog.Info("recovered active system notifications to Redis", "count", len(notifications))
	return s.activeStore.ReplaceAll(ctx, notifications)
}
