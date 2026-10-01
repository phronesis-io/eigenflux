package main

import (
	"context"
	"errors"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/item"
	"eigenflux_server/pkg/itemdispatch"
	profiledal "eigenflux_server/rpc/profile/dal"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type publishFixtureID struct{}

func publishContext() context.Context { return itemdispatch.WithDurableDispatch(context.Background()) }

func (publishFixtureID) NextID() (int64, error) { return 201, nil }

func publishRequest() *item.PublishItemReq {
	noReply := false
	return &item.PublishItemReq{AuthorAgentId: 101, RawContent: "A concrete official release.",
		RawNotes: strPtr(`{"source_type":"curated"}`), RawUrl: strPtr("https://example.org/release"), AcceptReply: &noReply}
}

func seedPublishAuthor(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	if !gdb.Migrator().HasTable(&itemdispatch.Outbox{}) {
		require.NoError(t, gdb.AutoMigrate(&itemdispatch.Outbox{}))
	}
	require.NoError(t, gdb.Create(&profiledal.Agent{AgentID: 101, Email: "publish@test.local",
		AgentName: "Publish author", CreatedAt: 1, UpdatedAt: 1}).Error)
}

func assertPublishRowCounts(t *testing.T, gdb *gorm.DB, want int64) {
	t.Helper()
	for _, table := range []string{"raw_items", "processed_items", "item_stats", "item_publish_outbox"} {
		var count int64
		require.NoError(t, gdb.Table(table).Where("item_id = ?", 201).Count(&count).Error)
		require.Equal(t, want, count, table)
	}
}

func TestPublishPersistenceRollsBackEveryWriteFailure(t *testing.T) {
	for _, table := range []string{"raw_items", "processed_items", "item_stats", "item_publish_outbox"} {
		t.Run(table, func(t *testing.T) {
			gdb := newDeleteItemTestDB(t)
			seedPublishAuthor(t, gdb)
			require.NoError(t, gdb.Callback().Create().After("gorm:create").Register("test:reject_publish", func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					tx.AddError(errors.New("injected persistence failure"))
				}
			}))
			resp, err := (&ItemServiceImpl{itemIDGen: publishFixtureID{}}).PublishItem(publishContext(), publishRequest())
			require.NoError(t, err)
			require.Equal(t, int32(500), resp.BaseResp.Code)
			require.Zero(t, resp.ItemId)
			assertPublishRowCounts(t, gdb, 0)
		})
	}
}

func TestPublishPersistenceCommitsQueryablePendingItem(t *testing.T) {
	gdb := newDeleteItemTestDB(t)
	seedPublishAuthor(t, gdb)
	svc := &ItemServiceImpl{itemIDGen: publishFixtureID{}}
	resp, err := svc.PublishItem(publishContext(), publishRequest())
	require.NoError(t, err)
	require.Zero(t, resp.BaseResp.Code)
	require.Equal(t, int64(201), resp.ItemId)
	assertPublishRowCounts(t, gdb, 1)
	listing, err := svc.GetMyItems(context.Background(), &item.GetMyItemsReq{AuthorAgentId: 101})
	require.NoError(t, err)
	require.Zero(t, listing.BaseResp.Code)
	require.Len(t, listing.Items, 1)
	require.Equal(t, resp.ItemId, listing.Items[0].ItemId)
	var pending struct {
		ExpectedResponse string
		Status           int16
	}
	require.NoError(t, gdb.Table("processed_items").Where("item_id = ?", 201).First(&pending).Error)
	require.Zero(t, pending.Status)
	require.Equal(t, "no_reply", pending.ExpectedResponse)
}

func TestPublishPersistenceHonorsCancelledContext(t *testing.T) {
	gdb := newDeleteItemTestDB(t)
	seedPublishAuthor(t, gdb)
	ctx, cancel := context.WithCancel(publishContext())
	cancel()
	resp, err := (&ItemServiceImpl{itemIDGen: publishFixtureID{}}).PublishItem(ctx, publishRequest())
	require.NoError(t, err)
	require.Equal(t, int32(500), resp.BaseResp.Code)
	assertPublishRowCounts(t, gdb, 0)
}
