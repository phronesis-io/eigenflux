package main

import (
	"context"
	"encoding/json"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	tradedal "eigenflux_server/rpc/trade/dal"
)

func StartOutboxDispatcher(ctx context.Context, cfg *config.Config) {
	interval := time.Duration(cfg.TradeOutboxDispatchIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Default().Info("outbox dispatcher started", "interval_ms", cfg.TradeOutboxDispatchIntervalMs)

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("outbox dispatcher stopped")
			return
		case <-ticker.C:
			dispatchPendingOutbox(ctx)
		}
	}
}

func dispatchPendingOutbox(ctx context.Context) {
	rows, err := tradedal.ListPendingOutbox(db.DB, 100)
	if err != nil {
		logger.Default().Warn("outbox list pending failed", "err", err)
		return
	}
	for _, row := range rows {
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil {
			logger.Default().Error("outbox payload invalid", "outbox_id", row.OutboxID, "err", err)
			_ = tradedal.MarkOutboxPublished(db.DB, row.OutboxID, time.Now().UnixMilli())
			continue
		}
		if _, err := mq.Publish(ctx, row.StreamName, payload); err != nil {
			logger.Default().Warn("outbox publish failed", "outbox_id", row.OutboxID, "err", err)
			continue
		}
		if err := tradedal.MarkOutboxPublished(db.DB, row.OutboxID, time.Now().UnixMilli()); err != nil {
			logger.Default().Error("outbox mark published failed", "outbox_id", row.OutboxID, "err", err)
		}
	}
}
