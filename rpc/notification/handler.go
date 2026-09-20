package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	if req == nil || req.GetAgentId() <= 0 {
		return &notificationrpc.ListPendingResp{
			Notifications: []*notificationrpc.PendingNotification{},
			BaseResp:      &base.BaseResp{Code: 400, Msg: "invalid request"},
		}, nil
	}
	limit := 0
	if req.IsSetLimit() {
		limit = int(req.GetLimit())
		if limit <= 0 || limit > 100 {
			return listPendingError(400, "limit must be between 1 and 100"), nil
		}
	}
	cursor, err := decodePendingCursor(req.GetCursor())
	if err != nil {
		return listPendingError(400, "invalid cursor"), nil
	}
	logger.Ctx(ctx).Debug("ListPending called", "agentID", req.GetAgentId())

	var all []*notificationrpc.PendingNotification

	// 1. Milestone notifications from Redis
	milestoneNotifs, err := dal.ListMilestoneNotifications(ctx, s.rdb, req.AgentId)
	if err != nil {
		logger.Ctx(ctx).Error("failed to list milestone notifications", "agentID", req.AgentId, "err", err)
	} else {
		for _, n := range milestoneNotifs {
			eventID, err := strconv.ParseInt(n.NotificationID, 10, 64)
			if err != nil {
				logger.Ctx(ctx).Warn("invalid milestone notification id", "notificationID", n.NotificationID, "err", err)
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
		logger.Ctx(ctx).Error("failed to list system notifications", "agentID", req.AgentId, "err", err)
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
		logger.Ctx(ctx).Error("failed to list PM notifications", "agentID", req.AgentId, "err", err)
	} else {
		for _, n := range pmNotifs {
			requestID, err := strconv.ParseInt(n.NotificationID, 10, 64)
			if err != nil {
				logger.Ctx(ctx).Warn("invalid PM notification id", "id", n.NotificationID, "err", err)
				continue
			}
			all = append(all, &notificationrpc.PendingNotification{
				NotificationId:  requestID,
				SourceType:      dal.SourceTypeFriendRequest,
				Type:            n.Type,
				Content:         n.Content,
				CreatedAt:       n.CreatedAt,
				PeerShortId:     optionalString(n.PeerShortID),
				PeerDisplayName: optionalString(n.PeerDisplayName),
				FriendUid:       optionalInt64(n.FriendUID),
			})
		}
	}

	// 4. Durable Commission Order notifications from PostgreSQL.
	inboxNotifications, err := dal.ListInboxNotifications(ctx, s.db, req.AgentId, time.Now().UnixMilli())
	if err != nil {
		logger.Ctx(ctx).Error("failed to list Commission Order notifications", "agentID", req.AgentId, "err", err)
		return listPendingError(500, "failed to list durable notifications"), nil
	}
	for i := range inboxNotifications {
		row := &inboxNotifications[i]
		payloadJSON := row.PayloadJSON
		all = append(all, &notificationrpc.PendingNotification{
			NotificationId: row.NotificationID,
			SourceType:     dal.SourceTypeCommissionOrder,
			Type:           row.EventKind,
			Content:        "",
			CreatedAt:      row.OccurredAt,
			PayloadJson:    &payloadJSON,
		})
	}

	// Sort by created_at ASC, notification_id ASC
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt != all[j].CreatedAt {
			return all[i].CreatedAt < all[j].CreatedAt
		}
		if all[i].NotificationId != all[j].NotificationId {
			return all[i].NotificationId < all[j].NotificationId
		}
		return all[i].SourceType < all[j].SourceType
	})
	if cursor != nil {
		filtered := all[:0]
		for _, notification := range all {
			if pendingAfterCursor(notification, *cursor) {
				filtered = append(filtered, notification)
			}
		}
		all = filtered
	}
	hasMore := false
	if limit > 0 && len(all) > limit {
		hasMore = true
		all = all[:limit]
	}

	if all == nil {
		all = []*notificationrpc.PendingNotification{}
	}

	response := &notificationrpc.ListPendingResp{
		Notifications: all,
		BaseResp:      &base.BaseResp{Code: 0, Msg: "success"},
	}
	if req.IsSetLimit() {
		response.HasMore = &hasMore
		if len(all) > 0 {
			nextCursor := encodePendingCursor(all[len(all)-1])
			response.NextCursor = &nextCursor
		}
	}
	return response, nil
}

type pendingCursor struct {
	CreatedAt      int64  `json:"created_at"`
	NotificationID int64  `json:"notification_id"`
	SourceType     string `json:"source_type"`
}

func listPendingError(code int32, message string) *notificationrpc.ListPendingResp {
	return &notificationrpc.ListPendingResp{Notifications: []*notificationrpc.PendingNotification{}, BaseResp: &base.BaseResp{Code: code, Msg: message}}
}

func decodePendingCursor(value string) (*pendingCursor, error) {
	if value == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	var cursor pendingCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.CreatedAt <= 0 || cursor.NotificationID <= 0 || cursor.SourceType == "" {
		return nil, fmt.Errorf("invalid pending cursor")
	}
	return &cursor, nil
}

func encodePendingCursor(notification *notificationrpc.PendingNotification) string {
	raw, _ := json.Marshal(pendingCursor{CreatedAt: notification.CreatedAt, NotificationID: notification.NotificationId, SourceType: notification.SourceType})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func pendingAfterCursor(notification *notificationrpc.PendingNotification, cursor pendingCursor) bool {
	if notification.CreatedAt != cursor.CreatedAt {
		return notification.CreatedAt > cursor.CreatedAt
	}
	if notification.NotificationId != cursor.NotificationID {
		return notification.NotificationId > cursor.NotificationID
	}
	return notification.SourceType > cursor.SourceType
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalInt64(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func (s *NotificationServiceImpl) AckNotifications(ctx context.Context, req *notificationrpc.AckNotificationsReq) (*notificationrpc.AckNotificationsResp, error) {
	if req == nil {
		return &notificationrpc.AckNotificationsResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "nil request"},
		}, nil
	}
	logger.Ctx(ctx).Debug("AckNotifications called", "agentID", req.GetAgentId(), "items", len(req.GetItems()))
	if len(req.Items) == 0 {
		return &notificationrpc.AckNotificationsResp{
			BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
		}, nil
	}

	var milestoneIDs []int64
	var systemItems []dal.NotificationDelivery
	var pmIDs []int64
	var commissionOrderIDs []int64
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
		case dal.SourceTypeCommissionOrder:
			commissionOrderIDs = append(commissionOrderIDs, item.NotificationId)
		default:
			logger.Ctx(ctx).Warn("unknown source_type in ack", "sourceType", item.SourceType)
		}
	}

	// Durable business notifications use strong acknowledgement semantics.
	// Validate ownership and persist them before applying legacy best-effort ACKs.
	if len(commissionOrderIDs) > 0 {
		if err := dal.AckInboxNotifications(ctx, s.db, req.AgentId, commissionOrderIDs, now); err != nil {
			logger.Ctx(ctx).Error("failed to acknowledge Commission Order notifications", "agentID", req.AgentId, "err", err)
			return &notificationrpc.AckNotificationsResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to acknowledge durable notifications"}}, nil
		}
	}

	// Ack milestone: delete from Redis + mark notified in DB
	if len(milestoneIDs) > 0 {
		if err := dal.DeleteMilestoneNotifications(ctx, s.rdb, req.AgentId, milestoneIDs); err != nil {
			logger.Ctx(ctx).Error("failed to delete milestone notifications from Redis", "agentID", req.AgentId, "err", err)
		}
		if err := dal.MarkMilestoneEventsNotified(ctx, s.db, req.AgentId, milestoneIDs, now); err != nil {
			logger.Ctx(ctx).Error("failed to mark milestone events notified", "agentID", req.AgentId, "err", err)
		}
	}

	// Ack system: insert delivery records
	if len(systemItems) > 0 {
		if err := dal.RecordDeliveries(ctx, s.db, systemItems); err != nil {
			logger.Ctx(ctx).Error("failed to record system notification deliveries", "agentID", req.AgentId, "err", err)
		}
	}

	// Ack friend request: delete from Redis
	if len(pmIDs) > 0 {
		if err := dal.DeletePMNotifications(ctx, s.rdb, req.AgentId, pmIDs); err != nil {
			logger.Ctx(ctx).Error("failed to delete PM notifications from Redis", "agentID", req.AgentId, "err", err)
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
				logger.Ctx(ctx).Warn("audience expression error", "notificationID", active[i].NotificationID, "err", err)
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
	logger.Default().Info("recovered active system notifications to Redis", "count", len(notifications))
	return s.activeStore.ReplaceAll(ctx, notifications)
}
