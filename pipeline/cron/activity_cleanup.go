package main

import (
	"context"
	"time"

	"eigenflux_server/api/dal"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"

	"github.com/redis/go-redis/v9"
)

const (
	lockKeyActivityCleanup = "lock:cron:activity_cleanup"
	activityRetentionDays  = 90
)

// StartActivityCleanup starts a daily cron job that deletes activity logs older than 90 days.
func StartActivityCleanup(ctx context.Context, rdb *redis.Client) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	// Run immediately on startup
	cleanupActivityWithLock(ctx, rdb)

	logger.Default().Info("activity cleanup cron started", "interval", "24h", "retention_days", activityRetentionDays)

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("activity cleanup cron stopped")
			return
		case <-ticker.C:
			cleanupActivityWithLock(ctx, rdb)
		}
	}
}

func cleanupActivityWithLock(ctx context.Context, rdb *redis.Client) {
	acquired, err := acquireLock(ctx, rdb, lockKeyActivityCleanup, 20*time.Minute)
	if err != nil {
		logger.Default().Warn("failed to acquire lock for activity cleanup", "err", err)
		return
	}
	if !acquired {
		logger.Default().Debug("activity cleanup skipped (another instance is running)")
		return
	}
	defer releaseLock(ctx, rdb, lockKeyActivityCleanup)

	cutoffMs := time.Now().AddDate(0, 0, -activityRetentionDays).UnixMilli()
	deleted, err := dal.DeleteOldActivityLogs(db.DB, cutoffMs)
	if err != nil {
		logger.Default().Error("failed to cleanup old activity logs", "err", err)
		return
	}

	logger.Default().Info("activity cleanup completed", "deleted", deleted)
}
