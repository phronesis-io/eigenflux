package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/rpc/notification/dal"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const commissionNotificationSchemaVersion = 1

type commissionOrderNotificationPayload struct {
	EventID            int64  `json:"event_id"`
	EventKind          string `json:"event_kind"`
	OrderID            int64  `json:"order_id"`
	OrderVersion       int64  `json:"order_version"`
	RecipientAgentID   int64  `json:"recipient_agent_id"`
	RecipientRole      string `json:"recipient_role"`
	State              string `json:"state"`
	FromState          string `json:"from_state"`
	ToState            string `json:"to_state"`
	CommandKind        string `json:"command_kind"`
	ActorKind          string `json:"actor_kind"`
	ActorAgentID       int64  `json:"actor_agent_id"`
	SnapshotID         int64  `json:"snapshot_id"`
	OccurredAt         int64  `json:"occurred_at"`
	PaymentDueAt       int64  `json:"payment_due_at"`
	SellerConfirmDueAt int64  `json:"seller_confirm_due_at"`
	DeliveryDueAt      int64  `json:"delivery_due_at"`
	BuyerConfirmDueAt  int64  `json:"buyer_confirm_due_at"`
}

type commissionOrderNotificationEvent struct {
	OutboxID, EventID, OrderID, OrderVersion, RecipientAgentID, OccurredAt int64
	EventKind, DedupeKey, PayloadJSON                                      string
}

type CommissionOrderNotificationConsumer struct {
	db      *gorm.DB
	rdb     *redis.Client
	runtime StreamConsumer
	now     func() time.Time
}

func NewCommissionOrderNotificationConsumer(cfg *config.Config, db *gorm.DB, rdb *redis.Client) *CommissionOrderNotificationConsumer {
	c := &CommissionOrderNotificationConsumer{db: db, rdb: rdb, now: time.Now}
	c.runtime = StreamConsumer{
		Name: "CommissionOrderNotificationConsumer", Stream: cfg.CommissionNotificationStream,
		Group: cfg.CommissionNotificationConsumerGroup, ConsumerName: "commission-order-notification",
		MetricsLabel: "commission:notification", Workers: cfg.CommissionNotificationConsumerWorkers,
		MaxRetries: int64(cfg.CommissionNotificationConsumerRetries), DeadLetterStream: cfg.CommissionNotificationDeadLetterStream,
		UnbufferedDispatch: true, FatalOnGroupCreateError: false, Handle: c.Handle,
	}
	return c
}

func (c *CommissionOrderNotificationConsumer) Start(ctx context.Context) { c.runtime.Run(ctx) }

func (c *CommissionOrderNotificationConsumer) Handle(ctx context.Context, _ string, values map[string]any) HandleResult {
	event, payload, err := parseCommissionOrderNotification(values)
	if err != nil {
		logger.Ctx(ctx).Warn("invalid Commission Order notification", "err", err)
		metrics.CommissionNotificationDeliveryTotal.WithLabelValues("invalid").Inc()
		return HandleFailure
	}
	now := c.now().UnixMilli()
	inserted, err := dal.InsertInboxNotification(ctx, c.db, &dal.InboxNotification{
		NotificationID: event.OutboxID, AgentID: event.RecipientAgentID,
		SourceType: dal.SourceTypeCommissionOrder, SourceID: event.OutboxID,
		DedupeKey: event.DedupeKey, EventKind: event.EventKind, OrderID: event.OrderID,
		OrderVersion: event.OrderVersion, PayloadJSON: event.PayloadJSON,
		OccurredAt: event.OccurredAt, ReceivedAt: now,
		ExpiresAt: now + dal.PendingRetention.Milliseconds(),
	})
	if err != nil {
		logger.Ctx(ctx).Error("failed to persist Commission Order notification", "outboxID", event.OutboxID, "err", err)
		metrics.CommissionNotificationDeliveryTotal.WithLabelValues("inbox_error").Inc()
		return HandleRetry
	}
	if !inserted {
		metrics.CommissionNotificationDeliveryTotal.WithLabelValues("duplicate").Inc()
		return HandleSuccess
	}
	metrics.CommissionNotificationDeliveryLatency.Observe(max(0, float64(now-payload.OccurredAt)/1000))
	wake, _ := json.Marshal(map[string]string{
		"source_type":     dal.SourceTypeCommissionOrder,
		"notification_id": strconv.FormatInt(event.OutboxID, 10),
		"sequence":        strconv.FormatInt(event.OutboxID, 10),
	})
	if err := c.rdb.Publish(ctx, fmt.Sprintf("notification:push:%d", event.RecipientAgentID), wake).Err(); err != nil {
		logger.Ctx(ctx).Warn("Commission Order notification wake-up failed", "notificationID", event.OutboxID, "err", err)
		metrics.CommissionNotificationDeliveryTotal.WithLabelValues("wake_error").Inc()
		if updateErr := dal.SetInboxDeliveryError(ctx, c.db, event.OutboxID, event.RecipientAgentID, err.Error()); updateErr != nil {
			logger.Ctx(ctx).Error("failed to record Commission Order wake-up error", "notificationID", event.OutboxID, "err", updateErr)
		}
		return HandleSuccess
	}
	metrics.CommissionNotificationDeliveryTotal.WithLabelValues("delivered").Inc()
	return HandleSuccess
}

func parseCommissionOrderNotification(values map[string]any) (commissionOrderNotificationEvent, commissionOrderNotificationPayload, error) {
	read := func(key string, maxLen int) (string, error) {
		value, ok := values[key]
		if !ok {
			return "", fmt.Errorf("missing %s", key)
		}
		var text string
		switch typed := value.(type) {
		case string:
			text = typed
		case []byte:
			text = string(typed)
		default:
			text = fmt.Sprint(typed)
		}
		if text == "" || len(text) > maxLen {
			return "", fmt.Errorf("invalid %s", key)
		}
		return text, nil
	}
	parseID := func(key string) (int64, error) {
		text, err := read(key, 32)
		if err != nil {
			return 0, err
		}
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil || value <= 0 {
			return 0, fmt.Errorf("invalid %s", key)
		}
		return value, nil
	}
	schema, err := parseID("schema_version")
	if err != nil || schema != commissionNotificationSchemaVersion {
		return commissionOrderNotificationEvent{}, commissionOrderNotificationPayload{}, errors.New("unsupported schema_version")
	}
	event := commissionOrderNotificationEvent{}
	if event.EventID, err = parseID("event_id"); err != nil {
		return event, commissionOrderNotificationPayload{}, err
	}
	if event.OutboxID, err = parseID("outbox_id"); err != nil {
		return event, commissionOrderNotificationPayload{}, err
	}
	if event.OrderID, err = parseID("order_id"); err != nil {
		return event, commissionOrderNotificationPayload{}, err
	}
	if event.OrderVersion, err = parseID("order_version"); err != nil {
		return event, commissionOrderNotificationPayload{}, err
	}
	if event.RecipientAgentID, err = parseID("recipient_agent_id"); err != nil {
		return event, commissionOrderNotificationPayload{}, err
	}
	if event.OccurredAt, err = parseID("occurred_at"); err != nil {
		return event, commissionOrderNotificationPayload{}, err
	}
	if event.EventKind, err = read("event_kind", 64); err != nil {
		return event, commissionOrderNotificationPayload{}, err
	}
	if event.DedupeKey, err = read("dedupe_key", 192); err != nil {
		return event, commissionOrderNotificationPayload{}, err
	}
	if event.PayloadJSON, err = read("payload_json", 16<<10); err != nil {
		return event, commissionOrderNotificationPayload{}, err
	}
	var payload commissionOrderNotificationPayload
	if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
		return event, payload, fmt.Errorf("decode payload_json: %w", err)
	}
	if payload.EventID != event.EventID || payload.EventKind != event.EventKind ||
		payload.OrderID != event.OrderID || payload.OrderVersion != event.OrderVersion ||
		payload.RecipientAgentID != event.RecipientAgentID || payload.OccurredAt != event.OccurredAt ||
		(payload.RecipientRole != "buyer" && payload.RecipientRole != "seller") ||
		payload.State == "" || payload.ToState == "" || payload.State != payload.ToState ||
		payload.ActorKind == "" || payload.SnapshotID <= 0 ||
		(payload.ActorKind == "system" && payload.ActorAgentID != 0) ||
		(payload.ActorKind != "system" && payload.ActorAgentID <= 0) {
		return event, payload, errors.New("envelope and payload disagree")
	}
	if event.EventKind == "order.state.changed.v1" && payload.CommandKind == "" {
		return event, payload, errors.New("state-change notification is missing command_kind")
	}
	if !dedupeKeyMatchesRecipient(event.DedupeKey, event.OrderID, event.RecipientAgentID) {
		return event, payload, errors.New("dedupe_key does not identify the order recipient")
	}
	return event, payload, nil
}

func dedupeKeyMatchesRecipient(key string, orderID, recipientID int64) bool {
	parts := strings.Split(key, ":")
	if len(parts) < 4 || parts[0] != "order" || parts[1] != strconv.FormatInt(orderID, 10) {
		return false
	}
	wantRecipient := strconv.FormatInt(recipientID, 10)
	for i := 2; i+1 < len(parts); i++ {
		if parts[i] == "recipient" {
			return parts[i+1] == wantRecipient
		}
	}
	return false
}
