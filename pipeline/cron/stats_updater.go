package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/stats"
	"eigenflux_server/rpc/sort/dal"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	lockKeyAgentCount = "lock:cron:agent_count"
	lockKeyCalibrator = "lock:cron:calibrator"
	lockTTL           = 8 * time.Minute // Lock expires before next run (10min interval)
)

// acquireLock attempts to acquire a distributed lock using Redis SET NX EX
func acquireLock(ctx context.Context, rdb *redis.Client, lockKey string, ttl time.Duration) (string, bool, error) {
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return "", false, fmt.Errorf("generate lock token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes[:])
	result, err := rdb.SetNX(ctx, lockKey, token, ttl).Result()
	if err != nil {
		return "", false, fmt.Errorf("failed to acquire lock: %w", err)
	}
	return token, result, nil
}

// releaseLock releases only the lock owned by token. A bounded independent
// context still releases after the caller's work context is cancelled.
func releaseLock(rdb *redis.Client, lockKey, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const compareAndDelete = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`
	if err := rdb.Eval(ctx, compareAndDelete, []string{lockKey}, token).Err(); err != nil {
		logger.Default().Warn("failed to release lock", "lockKey", lockKey, "err", err)
	}
}

func renewLock(ctx context.Context, rdb *redis.Client, lockKey, token string, ttl time.Duration) (bool, error) {
	const compareAndRenew = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("PEXPIRE", KEYS[1], ARGV[2]) else return 0 end`
	renewed, err := rdb.Eval(ctx, compareAndRenew, []string{lockKey}, token, ttl.Milliseconds()).Int64()
	return err == nil && renewed == 1, err
}

// startLockRenewal reports a lost lock by closing the returned channel. Callers
// must stop all writes when it closes: compare-and-delete only protects release,
// while fencing protects work that is still running after ownership changed.
func startLockRenewal(parent context.Context, rdb *redis.Client, lockKey, token string, ttl time.Duration) (func(), <-chan struct{}) {
	ctx, cancel := context.WithCancel(parent)
	lost := make(chan struct{})
	go func() {
		ticker := time.NewTicker(ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewed, err := renewLock(ctx, rdb, lockKey, token, ttl)
				if err != nil || !renewed {
					if err != nil && ctx.Err() == nil {
						logger.Default().Warn("failed to renew lock", "lockKey", lockKey, "err", err)
					}
					close(lost)
					return
				}
			}
		}
	}()
	return cancel, lost
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
	token, acquired, err := acquireLock(ctx, rdb, lockKeyAgentCount, lockTTL)
	if err != nil {
		logger.Default().Warn("failed to acquire lock for agent count update", "err", err)
		return
	}
	if !acquired {
		logger.Default().Debug("agent count update skipped (another instance is running)")
		return
	}
	defer releaseLock(rdb, lockKeyAgentCount, token)

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
	token, acquired, err := acquireLock(ctx, rdb, lockKeyCalibrator, lockTTL)
	if err != nil {
		logger.Default().Warn("failed to acquire lock for stats calibration", "err", err)
		return
	}
	if !acquired {
		logger.Default().Debug("stats calibration skipped (another instance is running)")
		return
	}
	defer releaseLock(rdb, lockKeyCalibrator, token)

	// Count total items from Elasticsearch
	itemCount, err := dal.CountItems(ctx)
	if err != nil {
		logger.Default().Error("failed to count items from ES", "err", err)
		return
	}

	// Match the Redis counter: every positive feedback (score 1 or 2) counts once.
	hqCount, err := countPositiveFeedback(db.DB)
	if err != nil {
		logger.Default().Error("failed to count positive feedback from item_stats", "err", err)
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

func countPositiveFeedback(database *gorm.DB) (int64, error) {
	var count int64
	err := database.Table("item_stats").
		Select("COALESCE(SUM(score_1_count + score_2_count), 0)").
		Scan(&count).Error
	return count, err
}
