package main

import (
	"context"
	"time"

	"eigenflux_server/pkg/consolev2retention"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"

	"github.com/redis/go-redis/v9"
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
