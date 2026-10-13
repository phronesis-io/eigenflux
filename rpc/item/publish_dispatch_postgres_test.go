package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"eigenflux_server/pkg/itemdispatch"
	"eigenflux_server/rpc/item/dal"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func dispatchFixture(t *testing.T) (*gorm.DB, *redis.Client) {
	t.Helper()
	gdb := publishPostgresDB(t)
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	resp, err := (&ItemServiceImpl{itemIDGen: publishFixtureID{}}).PublishItem(publishContext(), publishRequest())
	require.NoError(t, err)
	require.Zero(t, resp.BaseResp.Code)
	return gdb, rdb
}

func TestPostgresPublishDispatchRetriesRedisFailure(t *testing.T) {
	gdb, rdb := dispatchFixture(t)
	closed := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	require.NoError(t, closed.Close())
	require.Error(t, itemdispatch.Dispatch(context.Background(), gdb, closed, 201))
	var row itemdispatch.Outbox
	require.NoError(t, gdb.First(&row).Error)
	require.Nil(t, row.DispatchedAt)
	require.NoError(t, itemdispatch.Recover(context.Background(), gdb, rdb))
	require.Equal(t, int64(1), rdb.XLen(context.Background(), "stream:item:publish").Val())
	require.NoError(t, itemdispatch.Recover(context.Background(), gdb, rdb))
	require.Equal(t, int64(1), rdb.XLen(context.Background(), "stream:item:publish").Val())
}

func TestPostgresPublishLegacyGatewayRetainsDispatchOwnership(t *testing.T) {
	gdb := publishPostgresDB(t)
	resp, err := (&ItemServiceImpl{itemIDGen: publishFixtureID{}}).PublishItem(context.Background(), publishRequest())
	require.NoError(t, err)
	require.Zero(t, resp.BaseResp.Code)
	for _, table := range []string{"raw_items", "processed_items", "item_stats"} {
		var count int64
		require.NoError(t, gdb.Table(table).Count(&count).Error)
		require.Equal(t, int64(1), count)
	}
	var queued int64
	require.NoError(t, gdb.Model(&itemdispatch.Outbox{}).Count(&queued).Error)
	require.Zero(t, queued, "an older gateway performs its own direct Redis dispatch")
}

func TestPostgresPublishOutboxMigrationPreservesUndispatchedWork(t *testing.T) {
	gdb, rdb := dispatchFixture(t)
	sql, err := os.ReadFile("../../migrations/000111_item_publish_outbox.sql")
	require.NoError(t, err)
	down := strings.Split(string(sql), "-- +goose Down")[1]
	require.ErrorContains(t, gdb.Exec(down).Error, "Cannot remove undispatched item publications")
	assertPublishRowCounts(t, gdb, 1)
	require.Error(t, gdb.Create(&itemdispatch.Outbox{ItemID: 999, CreatedAt: 1}).Error, "foreign key rejects orphan work")
	require.NoError(t, itemdispatch.Recover(context.Background(), gdb, rdb))
	require.NoError(t, gdb.Exec(down).Error)
	require.False(t, gdb.Migrator().HasTable(&itemdispatch.Outbox{}))
}

func TestPostgresPublishDispatchLostSQLAckDoesNotDuplicate(t *testing.T) {
	gdb, rdb := dispatchFixture(t)
	require.NoError(t, gdb.Callback().Update().After("gorm:update").Register("test:reject_dispatch_ack", func(tx *gorm.DB) {
		if tx.Statement.Table == "item_publish_outbox" {
			tx.AddError(errors.New("injected ack failure"))
		}
	}))
	require.Error(t, itemdispatch.Dispatch(context.Background(), gdb, rdb, 201))
	require.Equal(t, int64(1), rdb.XLen(context.Background(), "stream:item:publish").Val())
	var row itemdispatch.Outbox
	require.NoError(t, gdb.First(&row).Error)
	require.Nil(t, row.DispatchedAt)
	require.NoError(t, gdb.Callback().Update().Remove("test:reject_dispatch_ack"))
	require.NoError(t, itemdispatch.Dispatch(context.Background(), gdb, rdb, 201))
	require.Equal(t, int64(1), rdb.XLen(context.Background(), "stream:item:publish").Val())
	require.NoError(t, gdb.First(&row).Error)
	require.NotNil(t, row.DispatchedAt)
}

func TestPostgresPublishDispatchReplicasSkipOwnedRow(t *testing.T) {
	gdb, rdb := dispatchFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	require.NoError(t, gdb.Callback().Update().Before("gorm:update").Register("test:hold_dispatch", func(tx *gorm.DB) {
		if tx.Statement.Table == "item_publish_outbox" {
			close(entered)
			<-release
		}
	}))
	done := make(chan error, 1)
	go func() { done <- itemdispatch.Dispatch(context.Background(), gdb, rdb, 201) }()
	<-entered
	err := itemdispatch.Dispatch(context.Background(), gdb, rdb, 201)
	close(release)
	require.NoError(t, err)
	require.NoError(t, <-done)
	require.Equal(t, int64(1), rdb.XLen(context.Background(), "stream:item:publish").Val())
}

func TestPostgresPublishRecoveryNeverReplaysLegacyPendingRows(t *testing.T) {
	gdb, rdb := dispatchFixture(t)
	require.NoError(t, dal.CreateRawItem(gdb, &dal.RawItem{ItemID: 202, AuthorAgentID: 101, RawContent: "A historical submission."}))
	require.NoError(t, dal.CreateProcessedItem(gdb, &dal.ProcessedItem{ItemID: 202, Status: dal.StatusPending}))
	require.NoError(t, itemdispatch.Recover(context.Background(), gdb, rdb))
	messages, err := rdb.XRange(context.Background(), "stream:item:publish", "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Equal(t, "201", messages[0].Values["item_id"])
	require.NoError(t, gdb.Callback().Delete().Before("gorm:delete").Register("test:reject_cleanup", func(tx *gorm.DB) {
		if tx.Statement.Table == "item_publish_outbox" {
			tx.AddError(errors.New("injected cleanup failure"))
		}
	}))
	// Seed an acknowledged row to simulate cleanup interrupted after DEL.
	now := int64(1)
	require.NoError(t, gdb.Create(&itemdispatch.Outbox{ItemID: 201, CreatedAt: 1, DispatchedAt: &now}).Error)
	require.Error(t, itemdispatch.Recover(context.Background(), gdb, rdb))
	require.NoError(t, gdb.Callback().Delete().Remove("test:reject_cleanup"))
	require.NoError(t, itemdispatch.Recover(context.Background(), gdb, rdb))
	require.Equal(t, int64(1), rdb.XLen(context.Background(), "stream:item:publish").Val())
}
