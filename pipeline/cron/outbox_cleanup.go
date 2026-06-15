package main

import (
	"context"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	tradedal "eigenflux_server/rpc/trade/dal"
)

func StartOutboxCleanup(ctx context.Context, cfg *config.Config) {
	interval := time.Duration(cfg.TradeOutboxCleanupIntervalSec) * time.Second
	if interval <= 0 {
		interval = time.Hour
	}
	retention := time.Duration(cfg.TradeOutboxRetentionDays) * 24 * time.Hour
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Default().Info("outbox cleanup started",
		"interval_sec", cfg.TradeOutboxCleanupIntervalSec,
		"retention_days", cfg.TradeOutboxRetentionDays)

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("outbox cleanup stopped")
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-retention).UnixMilli()
			n, err := tradedal.DeleteOldPublishedOutbox(db.DB, cutoff)
			if err != nil {
				logger.Default().Warn("outbox cleanup failed", "err", err)
				continue
			}
			if n > 0 {
				logger.Default().Info("outbox cleanup deleted rows", "count", n)
			}
		}
	}
}
