package main

import (
	"context"
	"time"

	"eigenflux_server/pkg/consolev2retention"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	lockKeyConsoleV2Cleanup = "lock:cron:console_v2_cleanup"
	consoleV2CleanupBatch   = 5000
	consoleV2CleanupMaxRuns = 100
)

// StartConsoleV2Cleanup keeps high-volume Feed snapshots bounded. One leader
// deletes small indexed batches; requests never perform retention work.
func StartConsoleV2Cleanup(ctx context.Context, rdb *redis.Client) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	cleanupConsoleV2WithLock(ctx, rdb)
	logger.Default().Info("Console V2 cleanup cron started", "interval", "10m", "batch_size", consoleV2CleanupBatch)
	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("Console V2 cleanup cron stopped")
			return
		case <-ticker.C:
			cleanupConsoleV2WithLock(ctx, rdb)
		}
	}
}

func cleanupConsoleV2WithLock(ctx context.Context, rdb *redis.Client) {
	token, acquired, err := acquireLock(ctx, rdb, lockKeyConsoleV2Cleanup, 5*time.Minute)
	if err != nil || !acquired {
		if err != nil {
			logger.Default().Warn("Console V2 cleanup lock failed", "err", err)
		}
		return
	}
	defer releaseLock(rdb, lockKeyConsoleV2Cleanup, token)

	deadline := time.Now().Add(2 * time.Minute)
	jobs := consolev2retention.Jobs()
	totals := make(map[string]int64, len(jobs))
	completed := make(map[string]bool, len(jobs))
	for run := 0; run < consoleV2CleanupMaxRuns && time.Now().Before(deadline); run++ {
		progress := false
		for _, job := range jobs {
			if completed[job.Name] || time.Now().After(deadline) {
				continue
			}
			statementCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			result := db.DB.WithContext(statementCtx).Exec(job.SQL, consoleV2CleanupBatch)
			cancel()
			if result.Error != nil {
				logger.Default().Error("Console V2 cleanup job failed", "job", job.Name, "err", result.Error)
				return
			}
			totals[job.Name] += result.RowsAffected
			completed[job.Name] = result.RowsAffected < consoleV2CleanupBatch
			progress = progress || result.RowsAffected > 0
		}
		if !progress {
			break
		}
	}
	for _, job := range jobs {
		if totals[job.Name] > 0 {
			logger.Default().Info("Console V2 cleanup completed", "job", job.Name, "rows", totals[job.Name])
		}
	}
}

func deleteTerminalFeedItems(gdb *gorm.DB, cutoff int64, limit int) (int64, error) {
	result := gdb.Exec(`WITH target AS (
		SELECT item.batch_item_id FROM feed_batch_items item
		JOIN feed_batches batch ON batch.batch_id = item.batch_id
		WHERE batch.status IN ('acked','dead','expired') AND batch.created_at < ?
		ORDER BY batch.created_at, item.batch_item_id LIMIT ?
	) DELETE FROM feed_batch_items item USING target
	WHERE item.batch_item_id = target.batch_item_id`, cutoff, limit)
	return result.RowsAffected, result.Error
}

func clearTerminalFeedStates(gdb *gorm.DB, cutoff int64, limit int) (int64, error) {
	result := gdb.Exec(`WITH target AS (
		SELECT state.agent_id, state.processing_scope FROM feed_consumer_state state
		JOIN feed_batches batch ON batch.batch_id = state.active_batch_id
		WHERE batch.status IN ('acked','dead','expired') AND batch.created_at < ?
		ORDER BY batch.created_at, batch.batch_id LIMIT ?
	) UPDATE feed_consumer_state state SET active_batch_id = NULL,
		updated_at = (extract(epoch FROM clock_timestamp())*1000)::bigint
	FROM target WHERE state.agent_id = target.agent_id AND state.processing_scope = target.processing_scope`, cutoff, limit)
	return result.RowsAffected, result.Error
}

func deleteTerminalFeedBatches(gdb *gorm.DB, cutoff int64, limit int) (int64, error) {
	result := gdb.Exec(`WITH target AS (
		SELECT batch.batch_id FROM feed_batches batch
		WHERE batch.status IN ('acked','dead','expired') AND batch.created_at < ?
		  AND NOT EXISTS (SELECT 1 FROM feed_consumer_state state WHERE state.active_batch_id = batch.batch_id)
		ORDER BY batch.created_at, batch.batch_id LIMIT ?
	) DELETE FROM feed_batches batch USING target WHERE batch.batch_id = target.batch_id`, cutoff, limit)
	return result.RowsAffected, result.Error
}
