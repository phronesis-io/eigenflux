package main

import (
	"context"
	"time"

	"eigenflux_server/api/dal"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"

	"github.com/redis/go-redis/v9"
)

const (
	lockKeyHighlightTranslate = "lock:cron:highlight_translate"
	// translateTopN mirrors the highlights endpoint (top 5) with headroom so
	// items about to enter the board are already warm.
	translateTopN = 8
	// translateBatchLimit bounds one run; leftovers are picked up next tick.
	translateBatchLimit = 60
	translateWorkers    = 4
)

// StartHighlightTranslate pre-translates the union of every agent's current
// top-served items into Chinese every 30 minutes, so zh-UI users get instant
// highlights instead of waiting on the first-view lazy translation.
func StartHighlightTranslate(ctx context.Context, cfg *config.Config, rdb *redis.Client, client *llm.Client) {
	if cfg.LLMApiKey == "" {
		logger.Default().Info("highlight translate cron disabled (no LLM_API_KEY)")
		return
	}
	tc := client.WithModel(cfg.LLMTranslateModel)

	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	// Run immediately on startup
	translateHighlightsWithLock(ctx, rdb, tc)

	logger.Default().Info("highlight translate cron started", "interval", "30m", "model", cfg.LLMTranslateModel)

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("highlight translate cron stopped")
			return
		case <-ticker.C:
			translateHighlightsWithLock(ctx, rdb, tc)
		}
	}
}

func translateHighlightsWithLock(ctx context.Context, rdb *redis.Client, tc *llm.Client) {
	acquired, err := acquireLock(ctx, rdb, lockKeyHighlightTranslate, 25*time.Minute)
	if err != nil {
		logger.Default().Warn("failed to acquire lock for highlight translate", "err", err)
		return
	}
	if !acquired {
		logger.Default().Debug("highlight translate skipped (another instance is running)")
		return
	}
	defer releaseLock(ctx, rdb, lockKeyHighlightTranslate)

	sinceMs := time.Now().Add(-24 * time.Hour).UnixMilli()
	items, err := dal.ListUntranslatedTopItems(db.DB, sinceMs, translateTopN, translateBatchLimit)
	if err != nil {
		logger.Default().Error("failed to list untranslated top items", "err", err)
		return
	}
	if len(items) == 0 {
		return
	}

	sem := make(chan struct{}, translateWorkers)
	done := 0
	for i := range items {
		it := items[i]
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			// Per-field language check: the pipeline may emit an English
			// summary for a Chinese source item (and vice versa). Fields that
			// are already Chinese are copied into the zh column so they leave
			// the candidate set.
			summaryZh, titleZh := it.SummaryZh, it.TitleZh
			if summaryZh == "" && it.Summary != "" {
				if dal.IsLikelyChinese(it.Summary) {
					summaryZh = it.Summary
				} else if zh, terr := tc.TranslateToChinese(ctx, it.Summary); terr == nil && zh != "" {
					summaryZh = zh
				} else if terr != nil {
					logger.Default().Warn("highlight summary translate failed", "itemID", it.ItemID, "err", terr)
				}
			}
			if titleZh == "" && it.RawContent != "" {
				preview := dal.PlainPreview(it.RawContent, 80)
				if dal.IsLikelyChinese(preview) {
					titleZh = preview
				} else if zh, terr := tc.TranslateToChinese(ctx, preview); terr == nil && zh != "" {
					titleZh = zh
				} else if terr != nil {
					logger.Default().Warn("highlight title translate failed", "itemID", it.ItemID, "err", terr)
				}
			}
			if summaryZh == it.SummaryZh && titleZh == it.TitleZh {
				return // nothing new; retried next tick
			}
			if uerr := dal.UpdateZhTranslations(db.DB, it.ItemID, summaryZh, titleZh); uerr != nil {
				logger.Default().Warn("highlight translate write-back failed", "itemID", it.ItemID, "err", uerr)
			}
		}()
		done++
	}
	// Drain the semaphore so the lock outlives all workers.
	for i := 0; i < translateWorkers; i++ {
		sem <- struct{}{}
	}

	logger.Default().Info("highlight translate completed", "candidates", len(items), "processed", done)
}
