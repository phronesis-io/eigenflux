package trade

import (
	"context"
	"fmt"
	"time"

	"eigenflux_server/pkg/json"

	"github.com/redis/go-redis/v9"
)

const (
	NotifyKeyPrefix = "trade:notify:"
	NotifyTTL       = 7 * 24 * time.Hour

	NotificationTypeOrderReceived  = "trade_order_received"
	NotificationTypeOrderDelivered = "trade_order_delivered"
)

// Notification is the JSON shape written to trade:notify:{agent_id} Redis
// hash. NotificationID is the outbox_id of the source trade event so the
// recipient can ack it via the gateway notification endpoint.
type Notification struct {
	NotificationID string `json:"notification_id"`
	Type           string `json:"type"`

	OrderID   int64  `json:"order_id"`
	ServiceID int64  `json:"service_id"`
	Title     string `json:"frozen_title"`
	CreatedAt int64  `json:"created_at"`

	BuyerAgentID int64  `json:"buyer_agent_id,omitempty"`
	BuyerInput   string `json:"buyer_input,omitempty"`

	SellerAgentID          int64  `json:"seller_agent_id,omitempty"`
	DeliveryPayloadPreview string `json:"delivery_payload_preview,omitempty"`
	FrozenAmountAtomic     int64  `json:"frozen_amount_atomic,omitempty"`
	FrozenAsset            string `json:"frozen_asset,omitempty"`
	DeliveredAt            int64  `json:"delivered_at,omitempty"`
}

func NotifyKey(agentID int64) string {
	return fmt.Sprintf("%s%d", NotifyKeyPrefix, agentID)
}

// WriteNotification HSETs the notification onto trade:notify:{agent_id} and
// refreshes the 7-day TTL. Mirrors pkg/milestone/notification.go.
func WriteNotification(ctx context.Context, rdb *redis.Client, agentID int64, n Notification) error {
	payload, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("marshal trade notification: %w", err)
	}
	key := NotifyKey(agentID)
	pipe := rdb.TxPipeline()
	pipe.HSet(ctx, key, n.NotificationID, payload)
	pipe.Expire(ctx, key, NotifyTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("write trade notification to redis: %w", err)
	}
	return nil
}
