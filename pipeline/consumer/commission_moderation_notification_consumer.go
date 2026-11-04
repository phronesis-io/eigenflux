package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/notificationpayload"
	"eigenflux_server/rpc/notification/dal"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type CommissionModerationNotificationConsumer struct {
	db      *gorm.DB
	rdb     *redis.Client
	runtime StreamConsumer
	now     func() time.Time
}

func CommissionModerationNotificationGroup(cfg *config.Config) string {
	return cfg.CommissionConsumerGroup + ":moderation-notification"
}

func NewCommissionModerationNotificationConsumer(cfg *config.Config, db *gorm.DB, rdb *redis.Client) *CommissionModerationNotificationConsumer {
	c := &CommissionModerationNotificationConsumer{db: db, rdb: rdb, now: time.Now}
	c.runtime = StreamConsumer{
		Name: "CommissionModerationNotificationConsumer", Stream: cfg.CommissionStream,
		Group: CommissionModerationNotificationGroup(cfg), ConsumerName: "commission-moderation-notification",
		MetricsLabel: "commission:moderation_notification", Workers: cfg.CommissionNotificationConsumerWorkers,
		MaxRetries: int64(cfg.CommissionNotificationConsumerRetries), DeadLetterStream: cfg.CommissionDeadLetterStream + ":moderation-notification",
		UnbufferedDispatch: true, FatalOnGroupCreateError: false, Handle: c.Handle,
	}
	return c
}

func (c *CommissionModerationNotificationConsumer) Start(ctx context.Context) { c.runtime.Run(ctx) }

func (c *CommissionModerationNotificationConsumer) Handle(ctx context.Context, _ string, values map[string]any) HandleResult {
	if streamText(values["topic"]) != notificationpayload.CommissionModerationTopic {
		return HandleSuccess
	}
	row, err := parseCommissionModerationNotification(values, c.now().UnixMilli())
	if err != nil {
		logger.Ctx(ctx).Warn("invalid Commission moderation notification", "err", err)
		return HandleFailure
	}
	inserted, err := dal.InsertInboxNotification(ctx, c.db, row)
	if err != nil {
		logger.Ctx(ctx).Error("failed to persist Commission moderation notification", "notificationID", row.NotificationID, "err", err)
		return HandleRetry
	}
	if !inserted {
		return HandleSuccess
	}
	wake, _ := json.Marshal(map[string]string{
		"source_type":     notificationpayload.SourceCommissionModeration,
		"notification_id": strconv.FormatInt(row.NotificationID, 10),
		"sequence":        strconv.FormatInt(row.NotificationID, 10),
	})
	if err := c.rdb.Publish(ctx, fmt.Sprintf("notification:push:%d", row.AgentID), wake).Err(); err != nil {
		logger.Ctx(ctx).Warn("Commission moderation notification wake-up failed", "notificationID", row.NotificationID)
		if err := dal.SetInboxDeliveryError(ctx, c.db, row.NotificationID, row.AgentID, "wake-up failed"); err != nil {
			logger.Ctx(ctx).Error("failed to record moderation wake-up error", "notificationID", row.NotificationID, "err", err)
		}
	}
	return HandleSuccess
}

func parseCommissionModerationNotification(values map[string]any, now int64) (*dal.InboxNotification, error) {
	invalid := errors.New("invalid Commission moderation event envelope")
	if streamText(values["schema_version"]) != "1" || streamText(values["topic"]) != notificationpayload.CommissionModerationTopic || streamText(values["aggregate_type"]) != "commission_moderation" {
		return nil, invalid
	}
	ids := make(map[string]int64, 4)
	for _, key := range []string{"event_id", "aggregate_id", "aggregate_version", "occurred_at"} {
		raw := streamText(values[key])
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 || strconv.FormatInt(value, 10) != raw {
			return nil, invalid
		}
		ids[key] = value
	}
	raw := streamText(values["payload_json"])
	payload, err := notificationpayload.ParseCommissionModeration(raw)
	if err != nil {
		return nil, err
	}
	if payload.ModerationID != streamText(values["aggregate_id"]) || payload.OccurredAt != ids["occurred_at"] || payload.NotificationVersion != ids["aggregate_version"] {
		return nil, invalid
	}
	recipient, _ := strconv.ParseInt(payload.RecipientAgentID, 10, 64)
	return &dal.InboxNotification{
		NotificationID: ids["event_id"], AgentID: recipient, SourceID: ids["event_id"],
		SourceType: notificationpayload.SourceCommissionModeration, EventKind: notificationpayload.CommissionModerationTopic,
		DedupeKey:   fmt.Sprintf("commission_moderation:%s:version:%d:recipient:%s", payload.ModerationID, ids["aggregate_version"], payload.RecipientAgentID),
		PayloadJSON: raw, OccurredAt: payload.OccurredAt, ReceivedAt: now, ExpiresAt: now + dal.PendingRetention.Milliseconds(),
	}, nil
}
