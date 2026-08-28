package main

import (
	"context"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"github.com/redis/go-redis/v9"
)

const (
	lockKeyPGCDemandSnapshot = "lock:cron:pgc_demand_snapshot"
	pgcDemandRefreshInterval = 15 * time.Minute
	pgcDemandRefreshLockTTL  = 20 * time.Minute
)

type pgcDemandRefreshFunc func(context.Context) error

func StartPGCDemandSnapshot(ctx context.Context, rdb *redis.Client) {
	refreshPGCDemandSnapshotWithLock(ctx, rdb, refreshPGCDemandSnapshot)
	ticker := time.NewTicker(pgcDemandRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshPGCDemandSnapshotWithLock(ctx, rdb, refreshPGCDemandSnapshot)
		}
	}
}

func refreshPGCDemandSnapshot(ctx context.Context) error {
	return db.DB.WithContext(ctx).
		Exec("REFRESH MATERIALIZED VIEW CONCURRENTLY grafana_pgc_demand_supply_24h_snapshot").
		Error
}

func refreshPGCDemandSnapshotWithLock(
	ctx context.Context,
	rdb *redis.Client,
	refresh pgcDemandRefreshFunc,
) bool {
	token, acquired, err := acquireLock(
		ctx, rdb, lockKeyPGCDemandSnapshot, pgcDemandRefreshLockTTL,
	)
	if err != nil {
		logger.Default().Warn("failed to acquire PGC demand snapshot lock", "err", err)
		return false
	}
	if !acquired {
		return false
	}
	defer releaseLock(rdb, lockKeyPGCDemandSnapshot, token)

	started := time.Now()
	if err := refresh(ctx); err != nil {
		logger.Default().Error("failed to refresh PGC demand snapshot", "err", err)
		return false
	}
	logger.Default().Info(
		"PGC demand snapshot refreshed",
		"duration", time.Since(started),
	)
	return true
}
