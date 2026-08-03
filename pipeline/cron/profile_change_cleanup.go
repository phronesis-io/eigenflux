package main

import (
	"context"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	profiledal "eigenflux_server/rpc/profile/dal"

	"github.com/redis/go-redis/v9"
)

const (
	lockKeyProfileChangeCleanup   = "lock:cron:profile_change_cleanup"
	profileChangeRetentionDays    = 90
	profileChangeCleanupBatchSize = 5000
)

// StartProfileChangeCleanup bounds profile audit growth without deleting the
// newest event for any field. The newest per-field record is durable because
// refresh-context needs its actor/time/previous-value metadata even when the
// field has not changed for more than the retention period.
func StartProfileChangeCleanup(ctx context.Context, rdb *redis.Client) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	cleanupProfileChangesWithLock(ctx, rdb)
	logger.Default().Info("profile change cleanup cron started", "interval", "24h", "retention_days", profileChangeRetentionDays)

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("profile change cleanup cron stopped")
			return
		case <-ticker.C:
			cleanupProfileChangesWithLock(ctx, rdb)
		}
	}
}

func cleanupProfileChangesWithLock(ctx context.Context, rdb *redis.Client) {
	acquired, err := acquireLock(ctx, rdb, lockKeyProfileChangeCleanup, 30*time.Minute)
	if err != nil {
		logger.Default().Warn("failed to acquire lock for profile change cleanup", "err", err)
		return
	}
	if !acquired {
		logger.Default().Debug("profile change cleanup skipped (another instance is running)")
		return
	}
	defer releaseLock(ctx, rdb, lockKeyProfileChangeCleanup)

	cutoffMs := time.Now().AddDate(0, 0, -profileChangeRetentionDays).UnixMilli()
	deleted, err := profiledal.DeleteSupersededProfileChangeEventsBefore(db.DB, cutoffMs, profileChangeCleanupBatchSize)
	if err != nil {
		logger.Default().Error("failed to cleanup superseded profile changes", "err", err, "deleted", deleted)
		return
	}
	logger.Default().Info("profile change cleanup completed", "deleted", deleted)
}
