package consumer

import (
	"container/list"
	"context"
	"strconv"
	"sync"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
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

func (c *OrderEventConsumer) handle(_ context.Context, _ string, values map[string]any) HandleResult {
	outboxID, _ := values["outbox_id"].(string)
	if c.seenOutbox(outboxID) {
		logger.Default().Debug("OrderEventConsumer skip duplicate by outbox_id", "outbox_id", outboxID)
		return HandleSuccess
	}

	serviceIDStr, ok := values["service_id"].(string)
	if !ok {
		logger.Default().Warn("OrderEventConsumer invalid message: missing service_id")
		return HandleFailure
	}

	serviceID, err := strconv.ParseInt(serviceIDStr, 10, 64)
	if err != nil {
		logger.Default().Warn("OrderEventConsumer invalid service_id", "serviceID", serviceIDStr)
		return HandleFailure
	}

	eventType, ok := values["event_type"].(string)
	if !ok {
		logger.Default().Warn("OrderEventConsumer invalid message: missing event_type")
		return HandleFailure
	}

	var column string
	switch eventType {
	case "released":
		column = "released_count"
	case "refunded":
		column = "refunded_count"
	case "expired":
		column = "expired_count"
	default:
		logger.Default().Warn("OrderEventConsumer unknown event_type, skipping", "eventType", eventType, "serviceID", serviceID)
		return HandleFailure
	}

	logger.Default().Info("OrderEventConsumer processing order event", "serviceID", serviceID, "eventType", eventType, "outbox_id", outboxID)

	if err := tradedal.IncrementServiceStats(db.DB, serviceID, column, time.Now().UnixMilli()); err != nil {
		logger.Default().Error("OrderEventConsumer failed to increment service stats", "serviceID", serviceID, "column", column, "err", err)
		return HandleFailure
	}

	if err := tradedal.UpdateServiceSuccessRate(db.DB, serviceID); err != nil {
		logger.Default().Error("OrderEventConsumer failed to update service success rate", "serviceID", serviceID, "err", err)
		return HandleFailure
	}

	logger.Default().Info("OrderEventConsumer processed order event", "serviceID", serviceID, "eventType", eventType)
	return HandleSuccess
}
