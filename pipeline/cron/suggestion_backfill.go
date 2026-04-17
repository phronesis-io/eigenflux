package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	itemDal "eigenflux_server/rpc/item/dal"

	"github.com/redis/go-redis/v9"
)

const (
	lockKeySuggestionBackfill          = "lock:cron:suggestion_backfill"
	defaultSuggestionBackfillBatchSize = 50
	defaultSuggestionBackfillInterval  = 10 * time.Minute
	defaultSuggestionBackfillWorkers   = 2
	defaultSuggestionBackfillPause     = 500 * time.Millisecond
)

type suggestionBackfillSettings struct {
	batchSize int
	interval  time.Duration
	workers   int
	pause     time.Duration
}

type suggestionBackfillResult int

const (
	suggestionBackfillSuccess suggestionBackfillResult = iota
	suggestionBackfillFailed
	suggestionBackfillSkipped
)

func suggestionBackfillSettingsFromConfig(cfg *config.Config) suggestionBackfillSettings {
	settings := suggestionBackfillSettings{
		batchSize: defaultSuggestionBackfillBatchSize,
		interval:  defaultSuggestionBackfillInterval,
		workers:   defaultSuggestionBackfillWorkers,
		pause:     defaultSuggestionBackfillPause,
	}
	if cfg == nil {
		return settings
	}
	if cfg.SuggestionBackfillBatchSize > 0 {
		settings.batchSize = cfg.SuggestionBackfillBatchSize
	}
	if cfg.SuggestionBackfillWorkers > 0 {
		settings.workers = cfg.SuggestionBackfillWorkers
	}
	if cfg.SuggestionBackfillPauseMs >= 0 {
		settings.pause = time.Duration(cfg.SuggestionBackfillPauseMs) * time.Millisecond
	}
	if cfg.SuggestionBackfillInterval != "" {
		if d, err := time.ParseDuration(cfg.SuggestionBackfillInterval); err == nil && d > 0 {
			settings.interval = d
		}
	}
	return settings
}

type backfillItem struct {
	ItemID           int64
	RawContent       string
	RawNotes         string
	Summary          string
	BroadcastType    string
	Domains          string
	Keywords         string
	Geo              string
	Timeliness       string
	ExpectedResponse string
}

func StartSuggestionBackfill(ctx context.Context, cfg *config.Config, rdb *redis.Client, llmClient *llm.Client) {
	settings := suggestionBackfillSettingsFromConfig(cfg)

	runSuggestionBackfill(ctx, rdb, llmClient, settings)

	ticker := time.NewTicker(settings.interval)
	defer ticker.Stop()

	logger.Default().Info(
		"suggestion backfill started",
		"interval", settings.interval.String(),
		"batchSize", settings.batchSize,
		"workers", settings.workers,
	)

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("suggestion backfill stopped")
			return
		case <-ticker.C:
			runSuggestionBackfill(ctx, rdb, llmClient, settings)
		}
	}
}

func runSuggestionBackfill(ctx context.Context, rdb *redis.Client, llmClient *llm.Client, settings suggestionBackfillSettings) {
	acquired, err := acquireLock(ctx, rdb, lockKeySuggestionBackfill, 25*time.Minute)
	if err != nil {
		logger.Default().Warn("suggestion backfill lock error", "err", err)
		return
	}
	if !acquired {
		logger.Default().Debug("suggestion backfill skipped (another instance running)")
		return
	}
	defer releaseLock(ctx, rdb, lockKeySuggestionBackfill)

	var items []backfillItem
	if err := db.DB.
		Table("processed_items").
		Select("processed_items.item_id, raw_items.raw_content, raw_items.raw_notes, processed_items.summary, processed_items.broadcast_type, processed_items.domains, processed_items.keywords, processed_items.geo, processed_items.timeliness, processed_items.expected_response").
		Joins("JOIN raw_items ON raw_items.item_id = processed_items.item_id").
		Where("processed_items.status = ? AND processed_items.suggestion IS NULL", itemDal.StatusCompleted).
		Limit(settings.batchSize).
		Find(&items).Error; err != nil {
		logger.Default().Error("suggestion backfill query failed", "err", err)
		return
	}

	if len(items) == 0 {
		logger.Default().Debug("suggestion backfill: nothing to do")
		return
	}

	logger.Default().Info("suggestion backfill starting", "items", len(items))

	itemCh := make(chan backfillItem)
	resultCh := make(chan suggestionBackfillResult, len(items))

	var wg sync.WaitGroup
	for i := 0; i < settings.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range itemCh {
				resultCh <- processSuggestionBackfillItem(ctx, item, llmClient, settings.pause)
			}
		}()
	}

	for _, item := range items {
		if ctx.Err() != nil {
			break
		}
		itemCh <- item
	}
	close(itemCh)

	wg.Wait()
	close(resultCh)

	success, failed := 0, 0
	for result := range resultCh {
		switch result {
		case suggestionBackfillSuccess:
			success++
		case suggestionBackfillFailed:
			failed++
		}
	}

	logger.Default().Info("suggestion backfill complete", "success", success, "failed", failed)
}

func processSuggestionBackfillItem(ctx context.Context, item backfillItem, llmClient *llm.Client, pause time.Duration) suggestionBackfillResult {
	if ctx.Err() != nil {
		return suggestionBackfillSkipped
	}

	input := llm.SuggestActionInput{
		Content:          item.RawContent,
		Notes:            item.RawNotes,
		Summary:          item.Summary,
		BroadcastType:    item.BroadcastType,
		Domains:          splitComma(item.Domains),
		Keywords:         splitComma(item.Keywords),
		Geo:              item.Geo,
		Timeliness:       item.Timeliness,
		ExpectedResponse: item.ExpectedResponse,
	}

	result, err := llmClient.SuggestAction(ctx, input)
	if pause > 0 {
		time.Sleep(pause)
	}
	if err != nil {
		logger.Default().Warn("suggestion backfill: LLM error", "itemID", item.ItemID, "err", err)
		return suggestionBackfillFailed
	}

	if err := itemDal.UpdateSuggestion(db.DB, item.ItemID, result.Suggestion); err != nil {
		logger.Default().Warn("suggestion backfill: DB error", "itemID", item.ItemID, "err", err)
		return suggestionBackfillFailed
	}

	logger.Default().Debug("suggestion backfill: done", "itemID", item.ItemID)
	return suggestionBackfillSuccess
}

func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
