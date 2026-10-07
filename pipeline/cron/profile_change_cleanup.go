package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	profiledal "eigenflux_server/rpc/profile/dal"

	"github.com/redis/go-redis/v9"
)

const (
	lockKeyProfileChangeCleanup    = "lock:cron:profile_change_cleanup"
	lastProfileChangeCleanupKey    = "cron:profile_change_cleanup:last_success"
	profileChangeRetentionDays     = 90
	profileChangeCleanupBatchSize  = 5000
	profileChangeCleanupMaxBatches = 20
	profileChangeCleanupTimeout    = 25 * time.Minute
	profileChangeCleanupRetry      = 15 * time.Minute
	profileChangeCleanupContinue   = time.Minute
)

var releaseProfileCleanupLockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

// StartProfileChangeCleanup bounds profile audit growth without deleting the
// newest event for any field. The newest per-field record is durable because
// refresh-context needs its actor/time/previous-value metadata even when the
// field has not changed for more than the retention period.
func StartProfileChangeCleanup(ctx context.Context, rdb *redis.Client) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	logger.Default().Info("profile change cleanup cron started", "interval", "24h", "retention_days", profileChangeRetentionDays)

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("profile change cleanup cron stopped")
			return
		case <-timer.C:
			next := cleanupProfileChangesWithLock(ctx, rdb)
			timer.Reset(next)
		}
	}
}

// cleanupProfileChangesWithLock returns the delay before the next attempt.
// Failures retry promptly; a saturated batch continues until the backlog is
// drained instead of silently waiting another day.
func cleanupProfileChangesWithLock(ctx context.Context, rdb *redis.Client) time.Duration {
	token, acquired, err := acquireProfileCleanupLock(ctx, rdb, 30*time.Minute)
	if err != nil {
		logger.Default().Warn("failed to acquire lock for profile change cleanup", "err", err)
		return profileChangeCleanupRetry
	}
	if !acquired {
		logger.Default().Debug("profile change cleanup skipped (another instance is running)")
		return profileChangeCleanupRetry
	}
	defer releaseProfileCleanupLock(rdb, token)
	if recentlyCompleted, checkErr := rdb.Exists(ctx, lastProfileChangeCleanupKey).Result(); checkErr != nil {
		logger.Default().Warn("failed to read profile change cleanup completion marker", "err", checkErr)
		return profileChangeCleanupRetry
	} else if recentlyCompleted > 0 {
		logger.Default().Debug("profile change cleanup skipped (completed within 24h)")
		return 24 * time.Hour
	}

	cleanupCtx, cancel := context.WithTimeout(ctx, profileChangeCleanupTimeout)
	defer cancel()
	startedAt := time.Now()
	cutoffMs := time.Now().AddDate(0, 0, -profileChangeRetentionDays).UnixMilli()
	trimmed, trimSaturated, err := profiledal.TrimSupersededProfileChangeEventPathsBefore(
		db.DB.WithContext(cleanupCtx), cutoffMs, profileChangeCleanupBatchSize, profileChangeCleanupMaxBatches,
	)
	if err != nil {
		logger.Default().Error("failed to trim superseded profile change paths", "err", err, "trimmed_rows", trimmed)
		return profileChangeCleanupRetry
	}
	deleted, deleteSaturated, err := profiledal.DeleteSupersededProfileChangeEventsBefore(
		db.DB.WithContext(cleanupCtx), cutoffMs, profileChangeCleanupBatchSize, profileChangeCleanupMaxBatches,
	)
	if err != nil {
		logger.Default().Error("failed to cleanup superseded profile changes", "err", err, "trimmed_rows", trimmed, "deleted", deleted)
		return profileChangeCleanupRetry
	}
	if trimSaturated || deleteSaturated {
		logger.Default().Warn("profile change cleanup batch limit reached; continuing shortly", "trimmed_rows", trimmed, "deleted", deleted, "duration", time.Since(startedAt))
		return profileChangeCleanupContinue
	}
	if markErr := rdb.Set(ctx, lastProfileChangeCleanupKey, time.Now().UnixMilli(), 24*time.Hour).Err(); markErr != nil {
		logger.Default().Warn("profile change cleanup completed but completion marker failed", "err", markErr, "trimmed_rows", trimmed, "deleted", deleted)
		return profileChangeCleanupRetry
	}
	logger.Default().Info("profile change cleanup completed", "trimmed_rows", trimmed, "deleted", deleted, "duration", time.Since(startedAt))
	return 24 * time.Hour
}

func acquireProfileCleanupLock(ctx context.Context, rdb *redis.Client, ttl time.Duration) (string, bool, error) {
	if rdb == nil {
		return "", false, fmt.Errorf("redis client is nil")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", false, fmt.Errorf("generate lock token: %w", err)
	}
	token := hex.EncodeToString(random)
	acquired, err := rdb.SetNX(ctx, lockKeyProfileChangeCleanup, token, ttl).Result()
	if err != nil {
		return "", false, err
	}
	return token, acquired, nil
}

func releaseProfileCleanupLock(rdb *redis.Client, token string) {
	if rdb == nil || token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := releaseProfileCleanupLockScript.Run(ctx, rdb, []string{lockKeyProfileChangeCleanup}, token).Err(); err != nil {
		logger.Default().Warn("failed to release profile change cleanup lock", "err", err)
	}
}
