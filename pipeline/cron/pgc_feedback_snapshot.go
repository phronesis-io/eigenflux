package main

import (
	"context"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"github.com/redis/go-redis/v9"
)

const (
	lockKeyPGCFeedbackSnapshot = "lock:cron:pgc_feedback_snapshot"
	pgcFeedbackRefreshInterval = 5 * time.Minute
	pgcFeedbackRefreshLockTTL  = 10 * time.Minute
)

type pgcFeedbackRefreshFunc func(context.Context) error

func StartPGCFeedbackSnapshot(ctx context.Context, rdb *redis.Client) {
	refreshPGCFeedbackSnapshotWithLock(ctx, rdb, refreshPGCFeedbackSnapshot)
	ticker := time.NewTicker(pgcFeedbackRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshPGCFeedbackSnapshotWithLock(ctx, rdb, refreshPGCFeedbackSnapshot)
		}
	}
}

func refreshPGCFeedbackSnapshot(ctx context.Context) error {
	return db.DB.WithContext(ctx).
		Exec("REFRESH MATERIALIZED VIEW CONCURRENTLY grafana_pgc_strong_feedback_daily").
		Error
}

func refreshPGCFeedbackSnapshotWithLock(
	ctx context.Context,
	rdb *redis.Client,
	refresh pgcFeedbackRefreshFunc,
) bool {
	token, acquired, err := acquireLock(
		ctx, rdb, lockKeyPGCFeedbackSnapshot, pgcFeedbackRefreshLockTTL,
	)
	if err != nil {
		logger.Default().Warn("failed to acquire PGC feedback snapshot lock", "err", err)
		return false
	}
	if !acquired {
		return false
	}
	defer releaseLock(rdb, lockKeyPGCFeedbackSnapshot, token)

	started := time.Now()
	if err := refresh(ctx); err != nil {
		logger.Default().Error("failed to refresh PGC feedback snapshot", "err", err)
		return false
	}
	logger.Default().Info(
		"PGC feedback snapshot refreshed",
		"duration", time.Since(started),
	)
	return true
}
