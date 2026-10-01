// Package itemdispatch durably bridges accepted publications to Redis Streams.
package itemdispatch

import (
	"context"
	"errors"
	"strconv"
	"time"

	"eigenflux_server/pkg/logger"
	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const stream = "stream:item:publish"

const dispatchKey = "item-publish-dispatch"

// WithDurableDispatch transfers stream ownership from an upgraded gateway to
// the RPC transaction. Older gateways retain their existing direct dispatcher.
func WithDurableDispatch(ctx context.Context) context.Context {
	return metainfo.WithPersistentValue(ctx, dispatchKey, "outbox-v1")
}

func DurableDispatchRequested(ctx context.Context) bool {
	value, ok := metainfo.GetPersistentValue(ctx, dispatchKey)
	return ok && value == "outbox-v1"
}

type Outbox struct {
	ItemID       int64 `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt    int64
	DispatchedAt *int64
}

func (Outbox) TableName() string { return "item_publish_outbox" }

// Enqueue must use the transaction that creates the raw/processed/stats rows.
func Enqueue(tx *gorm.DB, itemID int64) error {
	return tx.Create(&Outbox{ItemID: itemID, CreatedAt: time.Now().UnixMilli()}).Error
}

func marker(itemID int64) string { return "item:publish:dispatch:" + strconv.FormatInt(itemID, 10) }

// Keep uncertain Redis outcomes deduplicated until PostgreSQL confirms dispatch.
// The marker deliberately has no expiry before the database acknowledgement.
const publishScript = `
local previous = redis.call('GET', KEYS[2])
if previous then return previous end
local id = redis.call('XADD', KEYS[1], '*', 'item_id', ARGV[1])
redis.call('SET', KEYS[2], id)
return id`

// Dispatch serializes replicas on one durable row. Network and lock waits are
// bounded by the context; SKIP LOCKED lets another worker finish an owned item.
func Dispatch(ctx context.Context, gdb *gorm.DB, rdb *redis.Client, itemID int64) error {
	if rdb == nil {
		return errors.New("redis is not initialized")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Outbox
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("item_id = ? AND dispatched_at IS NULL", itemID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		deadline, _ := ctx.Deadline()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.DeadlineExceeded
		}
		if err := rdb.WithTimeout(remaining).Eval(ctx, publishScript, []string{stream, marker(itemID)}, strconv.FormatInt(itemID, 10)).Err(); err != nil {
			return err
		}
		return tx.Model(&Outbox{}).Where("item_id = ?", itemID).Update("dispatched_at", time.Now().UnixMilli()).Error
	})
}

// Recover processes at most 100 rows and stops on a dependency failure. It
// never derives new work from historical pending processed_items.
func Recover(ctx context.Context, gdb *gorm.DB, rdb *redis.Client) error {
	if rdb == nil {
		return errors.New("redis is not initialized")
	}
	var rows []Outbox
	if err := gdb.WithContext(ctx).Where("dispatched_at IS NULL").Order("item_id").Limit(100).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := Dispatch(ctx, gdb, rdb, row.ItemID); err != nil {
			return err
		}
	}
	// Acknowledged rows can be retired safely: a crash during cleanup never
	// makes them eligible for publishing again. Bound the Redis marker lifetime
	// only after the SQL acknowledgement is durable.
	if err := gdb.WithContext(ctx).Where("dispatched_at IS NOT NULL").Order("item_id").Limit(100).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := rdb.Del(ctx, marker(row.ItemID)).Err(); err != nil {
			return err
		}
		if err := gdb.WithContext(ctx).Where("item_id = ? AND dispatched_at IS NOT NULL", row.ItemID).Delete(&Outbox{}).Error; err != nil {
			return err
		}
	}
	return nil
}

func Run(ctx context.Context, gdb *gorm.DB, rdb *redis.Client) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		pass, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := Recover(pass, gdb, rdb)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Default().Warn("item publish dispatch recovery failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
