package consumer

import (
	"container/list"
	"context"
	"strconv"
	"sync"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	tradenotify "eigenflux_server/pkg/trade"
	tradedal "eigenflux_server/rpc/trade/dal"
)

const (
	orderEventStream       = "stream:trade:order-event"
	orderEventGroup        = "cg:trade:order-event"
	orderEventConsumerName = "order-event-worker-1"
	orderEventMetricsLabel = "trade:order-event"
	outboxIDLRUCap         = 10000
)

type OrderEventConsumer struct {
	runner    *StreamConsumer
	dedupMu   sync.Mutex
	dedupSeen map[string]*list.Element
	dedupLRU  *list.List
}

func NewOrderEventConsumer() *OrderEventConsumer {
	c := &OrderEventConsumer{
		dedupSeen: make(map[string]*list.Element, outboxIDLRUCap),
		dedupLRU:  list.New(),
	}
	c.runner = &StreamConsumer{
		Name:         "OrderEventConsumer",
		Stream:       orderEventStream,
		Group:        orderEventGroup,
		ConsumerName: orderEventConsumerName,
		MetricsLabel: orderEventMetricsLabel,
		Workers:      2,
		Handle:       c.handle,
	}
	return c
}

func (c *OrderEventConsumer) Start(ctx context.Context) { c.runner.Run(ctx) }

// seenOutbox returns true if outboxID has already been processed by this
// process in the recent LRU window. Side-effects: marks it as seen for next
// time. Empty outboxID never dedups (legacy messages from before this change).
func (c *OrderEventConsumer) seenOutbox(outboxID string) bool {
	if outboxID == "" {
		return false
	}
	c.dedupMu.Lock()
	defer c.dedupMu.Unlock()
	if elem, ok := c.dedupSeen[outboxID]; ok {
		c.dedupLRU.MoveToFront(elem)
		return true
	}
	elem := c.dedupLRU.PushFront(outboxID)
	c.dedupSeen[outboxID] = elem
	if c.dedupLRU.Len() > outboxIDLRUCap {
		oldest := c.dedupLRU.Back()
		if oldest != nil {
			c.dedupLRU.Remove(oldest)
			delete(c.dedupSeen, oldest.Value.(string))
		}
	}
	return false
}

func (c *OrderEventConsumer) handle(ctx context.Context, _ string, values map[string]any) HandleResult {
	outboxID, _ := values["outbox_id"].(string)
	if c.seenOutbox(outboxID) {
		logger.Default().Debug("OrderEventConsumer skip duplicate by outbox_id", "outbox_id", outboxID)
		return HandleSuccess
	}

	eventType, ok := values["event_type"].(string)
	if !ok {
		logger.Default().Warn("OrderEventConsumer invalid message: missing event_type")
		return HandleFailure
	}

	switch eventType {
	case "created":
		return c.handleCreated(ctx, outboxID, values)
	case "delivered":
		return c.handleDelivered(ctx, outboxID, values)
	case "released", "expired":
		return c.handleTerminal(outboxID, eventType, values)
	default:
		logger.Default().Warn("OrderEventConsumer unknown event_type, skipping", "eventType", eventType)
		return HandleFailure
	}
}

func (c *OrderEventConsumer) handleTerminal(outboxID, eventType string, values map[string]any) HandleResult {
	serviceIDStr, ok := values["service_id"].(string)
	if !ok {
		logger.Default().Warn("OrderEventConsumer missing service_id on terminal event")
		return HandleFailure
	}
	serviceID, err := strconv.ParseInt(serviceIDStr, 10, 64)
	if err != nil {
		logger.Default().Warn("OrderEventConsumer invalid service_id", "serviceID", serviceIDStr)
		return HandleFailure
	}

	var column string
	switch eventType {
	case "released":
		column = "released_count"
	case "expired":
		column = "expired_count"
	}

	logger.Default().Info("OrderEventConsumer processing terminal event", "serviceID", serviceID, "eventType", eventType, "outbox_id", outboxID)

	if err := tradedal.IncrementServiceStats(db.DB, serviceID, column, time.Now().UnixMilli()); err != nil {
		logger.Default().Error("OrderEventConsumer failed to increment service stats", "serviceID", serviceID, "column", column, "err", err)
		return HandleFailure
	}
	if err := tradedal.UpdateServiceSuccessRate(db.DB, serviceID); err != nil {
		logger.Default().Error("OrderEventConsumer failed to update service success rate", "serviceID", serviceID, "err", err)
		return HandleFailure
	}
	logger.Default().Info("OrderEventConsumer processed terminal event", "serviceID", serviceID, "eventType", eventType)
	return HandleSuccess
}

func (c *OrderEventConsumer) handleCreated(ctx context.Context, outboxID string, values map[string]any) HandleResult {
	orderStr, _ := values["order_id"].(string)
	serviceStr, _ := values["service_id"].(string)
	buyerStr, _ := values["buyer_agent_id"].(string)
	title, _ := values["frozen_title"].(string)
	buyerInput, _ := values["buyer_input"].(string)
	nowMsStr, _ := values["now_ms"].(string)

	orderID, err := strconv.ParseInt(orderStr, 10, 64)
	if err != nil {
		logger.Default().Warn("OrderEventConsumer created: bad order_id", "v", orderStr)
		return HandleFailure
	}
	serviceID, err := strconv.ParseInt(serviceStr, 10, 64)
	if err != nil {
		logger.Default().Warn("OrderEventConsumer created: bad service_id", "v", serviceStr)
		return HandleFailure
	}
	buyerID, err := strconv.ParseInt(buyerStr, 10, 64)
	if err != nil {
		logger.Default().Warn("OrderEventConsumer created: bad buyer_agent_id", "v", buyerStr)
		return HandleFailure
	}
	createdAt, _ := strconv.ParseInt(nowMsStr, 10, 64)

	svc, err := tradedal.GetService(db.DB, serviceID)
	if err != nil {
		logger.Default().Error("OrderEventConsumer created: load service", "serviceID", serviceID, "err", err)
		return HandleFailure
	}

	n := tradenotify.Notification{
		NotificationID: outboxID,
		Type:           tradenotify.NotificationTypeOrderReceived,
		OrderID:        orderID,
		ServiceID:      serviceID,
		Title:          title,
		BuyerAgentID:   buyerID,
		BuyerInput:     buyerInput,
		CreatedAt:      createdAt,
	}
	if err := tradenotify.WriteNotification(ctx, mq.RDB, svc.SellerAgentID, n); err != nil {
		logger.Default().Error("OrderEventConsumer created: write notif", "sellerID", svc.SellerAgentID, "err", err)
		return HandleFailure
	}
	logger.Default().Info("OrderEventConsumer wrote trade_order_received", "sellerID", svc.SellerAgentID, "orderID", orderID)
	return HandleSuccess
}

func (c *OrderEventConsumer) handleDelivered(ctx context.Context, outboxID string, values map[string]any) HandleResult {
	orderStr, _ := values["order_id"].(string)
	serviceStr, _ := values["service_id"].(string)
	sellerStr, _ := values["seller_agent_id"].(string)
	title, _ := values["frozen_title"].(string)
	preview, _ := values["delivery_payload_preview"].(string)
	amtStr, _ := values["frozen_amount_atomic"].(string)
	asset, _ := values["frozen_asset"].(string)
	nowMsStr, _ := values["now_ms"].(string)

	orderID, err := strconv.ParseInt(orderStr, 10, 64)
	if err != nil {
		logger.Default().Warn("OrderEventConsumer delivered: bad order_id", "v", orderStr)
		return HandleFailure
	}
	serviceID, err := strconv.ParseInt(serviceStr, 10, 64)
	if err != nil {
		logger.Default().Warn("OrderEventConsumer delivered: bad service_id", "v", serviceStr)
		return HandleFailure
	}
	sellerID, err := strconv.ParseInt(sellerStr, 10, 64)
	if err != nil {
		logger.Default().Warn("OrderEventConsumer delivered: bad seller_agent_id", "v", sellerStr)
		return HandleFailure
	}
	amount, _ := strconv.ParseInt(amtStr, 10, 64)
	deliveredAt, _ := strconv.ParseInt(nowMsStr, 10, 64)

	order, err := tradedal.GetOrder(db.DB, orderID)
	if err != nil {
		logger.Default().Error("OrderEventConsumer delivered: load order", "orderID", orderID, "err", err)
		return HandleFailure
	}

	n := tradenotify.Notification{
		NotificationID:         outboxID,
		Type:                   tradenotify.NotificationTypeOrderDelivered,
		OrderID:                orderID,
		ServiceID:              serviceID,
		Title:                  title,
		SellerAgentID:          sellerID,
		DeliveryPayloadPreview: preview,
		FrozenAmountAtomic:     amount,
		FrozenAsset:            asset,
		DeliveredAt:            deliveredAt,
		CreatedAt:              deliveredAt,
	}
	if err := tradenotify.WriteNotification(ctx, mq.RDB, order.BuyerAgentID, n); err != nil {
		logger.Default().Error("OrderEventConsumer delivered: write notif", "buyerID", order.BuyerAgentID, "err", err)
		return HandleFailure
	}
	logger.Default().Info("OrderEventConsumer wrote trade_order_delivered", "buyerID", order.BuyerAgentID, "orderID", orderID)
	return HandleSuccess
}
