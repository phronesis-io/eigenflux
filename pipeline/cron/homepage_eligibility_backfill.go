package main

import (
	"context"
	"sync"
	"time"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	itemDal "eigenflux_server/rpc/item/dal"

	"github.com/redis/go-redis/v9"
)

const (
	lockKeyHomepageEligibility            = "lock:cron:homepage_eligibility"
	defaultHomepageEligibilityBatch       = 32
	defaultHomepageEligibilityInterval    = 10 * time.Minute
	defaultHomepageEligibilityLookback    = 7 * 24 * time.Hour
	defaultHomepageEligibilityWorkers     = 1
	defaultHomepageEligibilityMinInterval = 5 * time.Second
	defaultHomepageEligibilityRetryDelay  = time.Hour
)

type homepageEligibilityBackfillItem struct {
	ItemID     int64  `gorm:"column:item_id"`
	RawContent string `gorm:"column:raw_content"`
	RawNotes   string `gorm:"column:raw_notes"`
}

func StartHomepageEligibilityBackfill(ctx context.Context, rdb *redis.Client, llmClient *llm.Client) {
	runHomepageEligibilityBackfill(ctx, rdb, llmClient)
	ticker := time.NewTicker(defaultHomepageEligibilityInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runHomepageEligibilityBackfill(ctx, rdb, llmClient)
		}
	}
}

func runHomepageEligibilityBackfill(ctx context.Context, rdb *redis.Client, llmClient *llm.Client) {
	token, acquired, err := acquireLock(ctx, rdb, lockKeyHomepageEligibility, 8*time.Minute)
	if err != nil || !acquired {
		if err != nil {
			logger.Default().Warn("homepage eligibility backfill lock error", "err", err)
		}
		return
	}
	defer releaseLock(rdb, lockKeyHomepageEligibility, token)

	now := time.Now()
	var items []homepageEligibilityBackfillItem
	if err := db.DB.Table("processed_items AS p").
		Select("p.item_id, r.raw_content, r.raw_notes").
		Joins("JOIN raw_items r ON r.item_id=p.item_id").
		Where(`p.status = ? AND r.created_at >= ?
			AND (p.homepage_evaluation_version <> ? OR p.homepage_evaluation_version IS NULL)
			AND (p.homepage_evaluation_retry_at IS NULL OR p.homepage_evaluation_retry_at <= ?)`,
			itemDal.StatusCompleted, homepageEligibilityWindowStart(now), llm.HomepageEvaluationV2, now.UnixMilli()).
		Order("p.updated_at DESC, p.item_id DESC").
		Limit(defaultHomepageEligibilityBatch).
		Find(&items).Error; err != nil {
		logger.Default().Error("homepage eligibility backfill query failed", "err", err)
		return
	}
	if len(items) == 0 {
		return
	}

	jobs := make(chan homepageEligibilityBackfillItem)
	rateLimiter := time.NewTicker(defaultHomepageEligibilityMinInterval)
	defer rateLimiter.Stop()
	var wg sync.WaitGroup
	for worker := 0; worker < defaultHomepageEligibilityWorkers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				select {
				case <-ctx.Done():
					return
				case <-rateLimiter.C:
				}
				result, err := llmClient.ProcessItem(ctx, item.RawContent, item.RawNotes)
				if err != nil {
					logger.Default().Warn("homepage eligibility backfill model error", "itemID", item.ItemID, "err", err)
					if retryErr := itemDal.ScheduleHomepageEvaluationRetry(db.DB, item.ItemID, homepageEligibilityRetryAt(time.Now())); retryErr != nil {
						logger.Default().Warn("homepage eligibility backfill retry schedule failed", "itemID", item.ItemID, "err", retryErr)
					}
					continue
				}
				if result.Discard {
					eligible := false
					result.HomepageEligible = &eligible
					result.HomepageRejectionReason = homepageReasonForDistributionDiscard(result.DiscardReason)
				}
				llm.NormalizeHomepageEvaluation(result)
				if result.HomepageEvaluationIncomplete {
					logger.Default().Warn("homepage eligibility backfill received incomplete evaluation", "itemID", item.ItemID)
					if retryErr := itemDal.ScheduleHomepageEvaluationRetry(db.DB, item.ItemID, homepageEligibilityRetryAt(time.Now())); retryErr != nil {
						logger.Default().Warn("homepage eligibility backfill retry schedule failed", "itemID", item.ItemID, "err", retryErr)
					}
					continue
				}
				if err := itemDal.UpdateHomepageEvaluation(db.DB, item.ItemID, llm.HomepageEligibleValue(result), llm.HomepageRealWorldRelevantValue(result), result.HomepageRejectionReason, result.HomepageEvaluationVersion); err != nil {
					logger.Default().Warn("homepage eligibility backfill DB error", "itemID", item.ItemID, "err", err)
				}
			}
		}()
	}

enqueue:
	for _, item := range items {
		select {
		case <-ctx.Done():
			break enqueue
		case jobs <- item:
		}
	}
	close(jobs)
	wg.Wait()
}

func homepageEligibilityRetryAt(now time.Time) int64 {
	return now.Add(defaultHomepageEligibilityRetryDelay).UnixMilli()
}

func homepageEligibilityWindowStart(now time.Time) int64 {
	return now.Add(-defaultHomepageEligibilityLookback).UnixMilli()
}

func homepageReasonForDistributionDiscard(reason string) string {
	switch reason {
	case "self_log":
		return "internal_log"
	case "spam":
		return "advertising"
	case "gibberish", "paywall":
		return "low_substance"
	default:
		return "other"
	}
}
