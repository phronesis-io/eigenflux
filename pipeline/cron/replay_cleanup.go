package main

import (
	"context"
	"time"

	"eigenflux_server/pipeline/consumer"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"

	"github.com/redis/go-redis/v9"
)

const (
	lockKeyReplayCleanup   = "lock:cron:replay_cleanup"
	replayCleanupBatchSize = 5000
)

// StartReplayCleanup runs a daily cron that purges replay_logs rows older than
// the configured retention. replay_logs is an append-only, high-volume table
// (hundreds of thousands of rows per day) with no other purge path; left
// unbounded it grows without limit. Deletes run in batches under a Redis lock
// so only one instance purges at a time.
func StartReplayCleanup(ctx context.Context, cfg *config.Config, rdb *redis.Client) {
	interval := time.Duration(cfg.ReplayLogCleanupIntervalSec) * time.Second
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	retentionDays := cfg.ReplayLogRetentionDays
	if retentionDays <= 0 {
		retentionDays = 30
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run immediately on startup
	cleanupReplayWithLock(ctx, rdb, retentionDays)

	logger.Default().Info("replay cleanup cron started",
		"interval_sec", cfg.ReplayLogCleanupIntervalSec, "retention_days", retentionDays)

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("replay cleanup cron stopped")
			return
		case <-ticker.C:
			cleanupReplayWithLock(ctx, rdb, retentionDays)
		}
	}
}

func cleanupReplayWithLock(ctx context.Context, rdb *redis.Client, retentionDays int) {
	acquired, err := acquireLock(ctx, rdb, lockKeyReplayCleanup, 30*time.Minute)
	if err != nil {
		logger.Default().Warn("failed to acquire lock for replay cleanup", "err", err)
		return
	}
	if !acquired {
		logger.Default().Debug("replay cleanup skipped (another instance is running)")
		return
	}
	defer releaseLock(ctx, rdb, lockKeyReplayCleanup)

	cutoffMs := time.Now().AddDate(0, 0, -retentionDays).UnixMilli()
	deleted, err := consumer.DeleteOldReplayLogs(db.DB, cutoffMs, replayCleanupBatchSize)
	if err != nil {
		logger.Default().Error("failed to cleanup old replay logs", "err", err, "deleted", deleted)
		return
	}

	logger.Default().Info("replay cleanup completed", "deleted", deleted)
}
