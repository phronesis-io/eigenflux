package main

import (
	"context"
	"errors"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	tradedal "eigenflux_server/rpc/trade/dal"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type idGen interface {
	NextID() (int64, error)
}

type ExpiryScanner struct {
	eventIDGen  idGen
	outboxIDGen idGen
}

func NewExpiryScanner(eventIDGen, outboxIDGen idGen) *ExpiryScanner {
	return &ExpiryScanner{eventIDGen: eventIDGen, outboxIDGen: outboxIDGen}
}

func StartTradeExpiryScanner(ctx context.Context, cfg *config.Config, _ *redis.Client, scanner *ExpiryScanner) {
	interval := time.Duration(cfg.TradeExpiryScanIntervalSec) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Default().Info("trade expiry scanner started", "interval_sec", cfg.TradeExpiryScanIntervalSec)

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("trade expiry scanner stopped")
			return
		case <-ticker.C:
			scanner.Tick()
		}
	}
}

func (s *ExpiryScanner) Tick() {
	nowMs := time.Now().UnixMilli()
	orders, err := tradedal.FindExpiredOrders(db.DB, nowMs, 100)
	if err != nil {
		logger.Default().Warn("trade expiry scan failed", "err", err)
		return
	}

	for _, order := range orders {
		if err := s.processOne(order, nowMs); err != nil {
			if errors.Is(err, tradedal.ErrTransitionConflict) {
				logger.Default().Debug("trade expiry: order already terminal (race)", "order_id", order.OrderID)
				continue
			}
			logger.Default().Warn("trade expiry update failed", "order_id", order.OrderID, "err", err)
			continue
		}
		logger.Default().Info("trade order expired", "order_id", order.OrderID)
	}
}

func (s *ExpiryScanner) processOne(order *tradedal.TradeOrder, nowMs int64) error {
	return db.DB.Transaction(func(tx *gorm.DB) error {
		if err := tradedal.TransitionOrderStatus(tx, order.OrderID, order.Status,
			tradedal.OrderStatusExpired, map[string]interface{}{"closed_at": nowMs}); err != nil {
			return err
		}
		evtID, err := s.eventIDGen.NextID()
		if err != nil {
			return err
		}
		if err := tradedal.CreateOrderEvent(tx, &tradedal.TradeOrderEvent{
			EventID:      evtID,
			OrderID:      order.OrderID,
			EventType:    tradedal.EventTypeExpired,
			ActorAgentID: 0,
		}); err != nil {
			return err
		}
		outID, err := s.outboxIDGen.NextID()
		if err != nil {
			return err
		}
		payload, err := tradedal.MarshalOrderEventPayload(outID, order.OrderID, order.ServiceID, "expired")
		if err != nil {
			return err
		}
		return tradedal.InsertOutbox(tx, &tradedal.TradeOutbox{
			OutboxID:    outID,
			StreamName:  "stream:trade:order-event",
			PayloadJSON: payload,
			CreatedAt:   nowMs,
		})
	})
}
