package dal

import (
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/redis/go-redis/v9"
)

const tradeNotifyKeyPrefix = "trade:notify:"

// TradeNotification is the shape stored in trade:notify:{agent_id} Redis hash.
// Field set mirrors pkg/trade.Notification; we keep a parallel struct here so
// the notification service does not import the trade producer package.
type TradeNotification struct {
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

func tradeNotifyKey(agentID int64) string {
	return fmt.Sprintf("%s%d", tradeNotifyKeyPrefix, agentID)
}

// ListTradeNotifications reads the trade:notify:{agentID} Redis hash.
func ListTradeNotifications(ctx context.Context, rdb *redis.Client, agentID int64) ([]TradeNotification, error) {
	values, err := rdb.HVals(ctx, tradeNotifyKey(agentID)).Result()
	if err != nil {
		return nil, fmt.Errorf("read trade notifications from redis: %w", err)
	}
	if len(values) == 0 {
		return nil, nil
	}

	notifications := make([]TradeNotification, 0, len(values))
	for _, value := range values {
		var n TradeNotification
		if err := json.Unmarshal([]byte(value), &n); err != nil {
			continue
		}
		notifications = append(notifications, n)
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

// DeleteTradeNotifications removes entries from the trade:notify:{agentID} Redis hash.
func DeleteTradeNotifications(ctx context.Context, rdb *redis.Client, agentID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	fields := make([]string, len(ids))
	for i, id := range ids {
		fields[i] = strconv.FormatInt(id, 10)
	}
	return rdb.HDel(ctx, tradeNotifyKey(agentID), fields...).Err()
}
