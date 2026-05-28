package dashboard

import (
	"context"
	"fmt"
	"log"
	"time"

	"console.eigenflux.ai/internal/dal"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	lockKeyDashboard = "lock:cron:dashboard_snapshot"
	lockTTL          = 5 * time.Minute
	cronInterval     = 24 * time.Hour
)

// StartSnapshotCron starts a background cron job that computes a dashboard snapshot every 24h.
func StartSnapshotCron(ctx context.Context, db *gorm.DB, rdb *redis.Client) {
	if err := dal.InitSnapshotDir(); err != nil {
		log.Printf("[dashboard] failed to init snapshot dir: %v", err)
		return
	}

	ticker := time.NewTicker(cronInterval)
	defer ticker.Stop()

	runWithLock(ctx, db, rdb)

	log.Println("[dashboard] snapshot cron started, interval=24h")

	for {
		select {
		case <-ctx.Done():
			log.Println("[dashboard] snapshot cron stopped")
			return
		case <-ticker.C:
			runWithLock(ctx, db, rdb)
		}
	}
}

// RunOnce computes a snapshot immediately (for manual refresh).
func RunOnce(db *gorm.DB) (int64, error) {
	data, err := ComputeSnapshot(db)
	if err != nil {
		return 0, fmt.Errorf("compute snapshot: %w", err)
	}

	now := time.Now().UnixMilli()
	id, err := dal.CreateSnapshot(data, now)
	if err != nil {
		return 0, fmt.Errorf("create snapshot: %w", err)
	}

	log.Printf("[dashboard] snapshot created: id=%d", id)
	return id, nil
}

func runWithLock(ctx context.Context, db *gorm.DB, rdb *redis.Client) {
	acquired, err := rdb.SetNX(ctx, lockKeyDashboard, time.Now().Unix(), lockTTL).Result()
	if err != nil {
		log.Printf("[dashboard] failed to acquire lock: %v", err)
		return
	}
	if !acquired {
		log.Println("[dashboard] snapshot skipped (another instance is running)")
		return
	}
	defer rdb.Del(ctx, lockKeyDashboard)

	data, err := ComputeSnapshot(db)
	if err != nil {
		log.Printf("[dashboard] failed to compute snapshot: %v", err)
		return
	}

	now := time.Now().UnixMilli()
	id, err := dal.CreateSnapshot(data, now)
	if err != nil {
		log.Printf("[dashboard] failed to create snapshot: %v", err)
		return
	}

	log.Printf("[dashboard] snapshot created: id=%d, size=%d bytes", id, len(data))
}
