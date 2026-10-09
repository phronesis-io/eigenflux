package main

import (
	"context"
	"fmt"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/recommendationmetrics"
	"github.com/redis/go-redis/v9"
)

const lockKeyRecommendationEffect = "lock:cron:recommendation_effect"

// recommendationEffectInterval keeps the default 24-hour activity page within
// 15 minutes. Each batch refreshes today and yesterday; past days are refreshed
// only until final (see recommendationmetrics.PendingDays), so the day that
// crosses day+3 is finalized by the first batch after Shanghai midnight.
const recommendationEffectInterval = 15 * time.Minute

// StartRecommendationEffect refreshes anonymous observations in serial bounded
// day batches containing daily and hourly activity. Grafana reads only the completed aggregates, never fact tables.
func StartRecommendationEffect(ctx context.Context, rdb *redis.Client) {
	refreshRecommendationEffectWithLock(ctx, rdb, refreshRecommendationEffect)
	ticker := time.NewTicker(recommendationEffectInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshRecommendationEffectWithLock(ctx, rdb, refreshRecommendationEffect)
		}
	}
}

func refreshRecommendationEffect(ctx context.Context) error {
	conn, err := db.DB.DB()
	if err != nil {
		return err
	}
	cutoff := time.Now()
	days, err := recommendationmetrics.PendingDays(ctx, conn, cutoff)
	if err != nil {
		return err
	}
	var firstError error
	for _, day := range days {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := recommendationmetrics.RefreshDay(ctx, conn, day, cutoff); err != nil {
			logger.Default().Warn("recommendation observation day failed", "day", day, "err", err)
			if firstError == nil {
				firstError = err
			}
		}
	}

	historyDays, historyErr := recommendationmetrics.HistoryPendingDays(ctx, conn, cutoff)
	if historyErr != nil {
		return fmt.Errorf("select feedback history days: %w", historyErr)
	}
	for _, day := range historyDays {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := recommendationmetrics.RefreshHistoryDay(ctx, conn, day, cutoff); err != nil {
			logger.Default().Warn("feedback history day failed", "day", day, "err", err)
			if firstError == nil {
				firstError = err
			}
		}
	}
	return firstError
}

func refreshRecommendationEffectWithLock(ctx context.Context, rdb *redis.Client, refresh func(context.Context) error) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	token, acquired, err := acquireLock(ctx, rdb, lockKeyRecommendationEffect, 5*time.Minute)
	if err != nil || !acquired {
		if err != nil {
			logger.Default().Warn("recommendation observation lock failed", "err", err)
		}
		return false
	}
	defer releaseLock(rdb, lockKeyRecommendationEffect, token)
	started := time.Now()
	if err := refresh(ctx); err != nil {
		logger.Default().Warn("recommendation observation batch incomplete", "err", err)
		return false
	}
	logger.Default().Info("recommendation observation batch refreshed", "duration", time.Since(started))
	return true
}
