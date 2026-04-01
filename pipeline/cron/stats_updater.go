package main

import (
	"context"
	"fmt"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/stats"
	"eigenflux_server/rpc/sort/dal"

	"github.com/redis/go-redis/v9"
)

const (
	lockKeyAgentCount = "lock:cron:agent_count"
	lockKeyCalibrator = "lock:cron:calibrator"
	lockTTL           = 8 * time.Minute // Lock expires before next run (10min interval)
)

// acquireLock attempts to acquire a distributed lock using Redis SET NX EX
func acquireLock(ctx context.Context, rdb *redis.Client, lockKey string, ttl time.Duration) (bool, error) {
	result, err := rdb.SetNX(ctx, lockKey, time.Now().Unix(), ttl).Result()
	if err != nil {
		return false, fmt.Errorf("failed to acquire lock: %w", err)
	}
	return result, nil
}

// releaseLock releases the distributed lock
func releaseLock(ctx context.Context, rdb *redis.Client, lockKey string) {
	if err := rdb.Del(ctx, lockKey).Err(); err != nil {
		logger.Default().Warn("failed to release lock", "lockKey", lockKey, "err", err)
	}
}

// StartAgentCountUpdater starts a cron job that updates agent count every minute
func StartAgentCountUpdater(ctx context.Context, cfg *config.Config, rdb *redis.Client) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	// Run immediately on startup
	updateAgentCountWithLock(ctx, rdb)

	logger.Default().Info("agent count updater started", "interval", "10m")

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("agent count updater stopped")
			return
		case <-ticker.C:
			updateAgentCountWithLock(ctx, rdb)
		}
	}
}

func updateAgentCountWithLock(ctx context.Context, rdb *redis.Client) {
	// Try to acquire lock
	acquired, err := acquireLock(ctx, rdb, lockKeyAgentCount, lockTTL)
	if err != nil {
		logger.Default().Warn("failed to acquire lock for agent count update", "err", err)
		return
	}
	if !acquired {
		logger.Default().Debug("agent count update skipped (another instance is running)")
		return
	}
	defer releaseLock(ctx, rdb, lockKeyAgentCount)

	var count int64
	if err := db.DB.Model(&struct {
		AgentID int64 `gorm:"column:agent_id"`
	}{}).Table("agents").Count(&count).Error; err != nil {
		logger.Default().Error("failed to count agents", "err", err)
		return
	}

	if err := stats.SetAgentCount(ctx, rdb, count); err != nil {
		logger.Default().Error("failed to update agent count in Redis", "err", err)
		return
	}

	// Calibrate agent countries from PG
	var countries []string
	if err := db.DB.Model(&struct {
		Country string `gorm:"column:country"`
	}{}).Table("agent_profiles").
		Where("country != ''").
		Distinct("country").
		Pluck("country", &countries).Error; err != nil {
		logger.Default().Warn("failed to query distinct countries", "err", err)
	} else {
		if err := stats.CalibrateAgentCountries(ctx, rdb, countries); err != nil {
			logger.Default().Warn("failed to calibrate agent countries in Redis", "err", err)
		}
	}

	logger.Default().Info("agent count updated", "count", count, "countries", countries)
}

// StartStatsCalibrator starts a cron job that calibrates stats from Elasticsearch every 10 minutes
func StartStatsCalibrator(ctx context.Context, cfg *config.Config, rdb *redis.Client) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	// Run immediately on startup
	calibrateStatsWithLock(ctx, rdb)

	logger.Default().Info("stats calibrator started", "interval", "10m")

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("stats calibrator stopped")
			return
		case <-ticker.C:
			calibrateStatsWithLock(ctx, rdb)
		}
	}
}

func calibrateStatsWithLock(ctx context.Context, rdb *redis.Client) {
	// Try to acquire lock
	acquired, err := acquireLock(ctx, rdb, lockKeyCalibrator, lockTTL)
	if err != nil {
		logger.Default().Warn("failed to acquire lock for stats calibration", "err", err)
		return
	}
	if !acquired {
		logger.Default().Debug("stats calibration skipped (another instance is running)")
		return
	}
	defer releaseLock(ctx, rdb, lockKeyCalibrator)

	// Count total items from Elasticsearch
	itemCount, err := dal.CountItems(ctx)
	if err != nil {
		logger.Default().Error("failed to count items from ES", "err", err)
		return
	}

	// Count high-quality items from item_stats table (score_1_count > 0 OR score_2_count > 0)
	var hqCount int64
	if err := db.DB.Model(&struct {
		ItemID int64 `gorm:"column:item_id"`
	}{}).Table("item_stats").
		Where("score_1_count > 0 OR score_2_count > 0").
		Count(&hqCount).Error; err != nil {
		logger.Default().Error("failed to count high-quality items from item_stats", "err", err)
		return
	}

	// Update Redis
	if err := stats.SetItemTotal(ctx, rdb, itemCount); err != nil {
		logger.Default().Error("failed to calibrate item total in Redis", "err", err)
		return
	}

	if err := stats.SetHighQualityCount(ctx, rdb, hqCount); err != nil {
		logger.Default().Error("failed to calibrate high-quality count in Redis", "err", err)
		return
	}

	logger.Default().Info("stats calibrated", "items", itemCount, "highQuality", hqCount)
}
