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
	lockKeyHomepageEligibility         = "lock:cron:homepage_eligibility"
	defaultHomepageEligibilityBatch    = 32
	defaultHomepageEligibilityInterval = 10 * time.Minute
	defaultHomepageEligibilityWorkers  = 2
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

	var items []homepageEligibilityBackfillItem
	if err := db.DB.Table("processed_items AS p").
		Select("p.item_id, r.raw_content, r.raw_notes").
		Joins("JOIN raw_items r ON r.item_id=p.item_id").
		Where("p.status = ? AND p.updated_at >= ? AND (p.homepage_evaluation_version <> ? OR p.homepage_evaluation_version IS NULL)",
			itemDal.StatusCompleted, time.Now().Add(-30*24*time.Hour).UnixMilli(), llm.HomepageEvaluationV1).
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
	var wg sync.WaitGroup
	for worker := 0; worker < defaultHomepageEligibilityWorkers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				result, err := llmClient.ProcessItem(ctx, item.RawContent, item.RawNotes)
				if err != nil {
					logger.Default().Warn("homepage eligibility backfill model error", "itemID", item.ItemID, "err", err)
					continue
				}
				if result.Discard {
					result.HomepageEligible = false
					result.HomepageRejectionReason = homepageReasonForDistributionDiscard(result.DiscardReason)
				}
				llm.NormalizeHomepageEvaluation(result)
				if err := itemDal.UpdateHomepageEvaluation(db.DB, item.ItemID, result.HomepageEligible, result.HomepageRejectionReason, result.HomepageEvaluationVersion); err != nil {
					logger.Default().Warn("homepage eligibility backfill DB error", "itemID", item.ItemID, "err", err)
				}
			}
		}()
	}
	for _, item := range items {
		if ctx.Err() != nil {
			break
		}
		jobs <- item
	}
	close(jobs)
	wg.Wait()
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
