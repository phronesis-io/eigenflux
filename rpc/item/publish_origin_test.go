package main

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"eigenflux_server/pkg/publishorigin"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func storedPublishOrigin(t *testing.T, gdb *gorm.DB) sql.NullString {
	t.Helper()
	var origin sql.NullString
	require.NoError(t, gdb.Raw("SELECT publish_origin FROM raw_items WHERE item_id = ?", 201).Row().Scan(&origin))
	return origin
}

// The DAL model does not map publish_origin; SQLite fixtures add the column the
// way migration 000115 does.
func addPublishOriginColumn(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	require.NoError(t, gdb.Exec("ALTER TABLE raw_items ADD COLUMN publish_origin TEXT").Error)
}

func TestPublishRecordsRecognizedOrigin(t *testing.T) {
	for _, origin := range []string{publishorigin.Heartbeat, publishorigin.Owner} {
		t.Run(origin, func(t *testing.T) {
			gdb := newDeleteItemTestDB(t)
			seedPublishAuthor(t, gdb)
			addPublishOriginColumn(t, gdb)
			ctx := publishorigin.WithOrigin(publishContext(), origin)
			resp, err := (&ItemServiceImpl{itemIDGen: publishFixtureID{}}).PublishItem(ctx, publishRequest())
			require.NoError(t, err)
			require.Zero(t, resp.BaseResp.Code)
			assertPublishRowCounts(t, gdb, 1)
			require.Equal(t, sql.NullString{String: origin, Valid: true}, storedPublishOrigin(t, gdb))
		})
	}
}

func TestPublishStoresNullForAbsentOrUnrecognizedOrigin(t *testing.T) {
	cases := map[string]context.Context{
		"absent": publishContext(),
		// A gateway bug or a foreign caller cannot smuggle an arbitrary value.
		"unrecognized": metainfo.WithValue(publishContext(), "item-publish-origin", "scheduled"),
	}
	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			gdb := newDeleteItemTestDB(t)
			seedPublishAuthor(t, gdb)
			addPublishOriginColumn(t, gdb)
			resp, err := (&ItemServiceImpl{itemIDGen: publishFixtureID{}}).PublishItem(ctx, publishRequest())
			require.NoError(t, err)
			require.Zero(t, resp.BaseResp.Code)
			assertPublishRowCounts(t, gdb, 1)
			require.False(t, storedPublishOrigin(t, gdb).Valid)
		})
	}
}

// Origin is telemetry: before migration 000115 (or after its Down) every
// publish, with or without an origin, still succeeds and writes every row.
func TestPublishSucceedsBeforeColumnExists(t *testing.T) {
	for _, origin := range []string{"", publishorigin.Heartbeat, publishorigin.Owner} {
		t.Run("origin="+origin, func(t *testing.T) {
			gdb := newDeleteItemTestDB(t)
			seedPublishAuthor(t, gdb)
			ctx := publishorigin.WithOrigin(publishContext(), origin)
			resp, err := (&ItemServiceImpl{itemIDGen: publishFixtureID{}}).PublishItem(ctx, publishRequest())
			require.NoError(t, err)
			require.Zero(t, resp.BaseResp.Code)
			assertPublishRowCounts(t, gdb, 1)
		})
	}
}

func TestPostgresPublishOriginAcrossMigration000115(t *testing.T) {
	gdb := publishPostgresDB(t)
	svc := &ItemServiceImpl{itemIDGen: publishFixtureID{}}

	// Before the migration: a heartbeat publish still commits every row; the
	// failed origin update is confined to its savepoint.
	resp, err := svc.PublishItem(publishorigin.WithOrigin(publishContext(), publishorigin.Heartbeat), publishRequest())
	require.NoError(t, err)
	require.Zero(t, resp.BaseResp.Code)
	assertPublishRowCounts(t, gdb, 1)
	require.NoError(t, gdb.Exec("DELETE FROM item_publish_outbox").Error)
	require.NoError(t, gdb.Exec("DELETE FROM item_stats").Error)
	require.NoError(t, gdb.Exec("DELETE FROM processed_items").Error)
	require.NoError(t, gdb.Exec("DELETE FROM raw_items").Error)

	migration, err := os.ReadFile("../../migrations/000115_raw_items_publish_origin.sql")
	require.NoError(t, err)
	up, down, ok := strings.Cut(string(migration), "-- +goose Down")
	require.True(t, ok)
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error { return tx.Exec(up).Error }))
	// Up is idempotent.
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error { return tx.Exec(up).Error }))

	resp, err = svc.PublishItem(publishorigin.WithOrigin(publishContext(), publishorigin.Heartbeat), publishRequest())
	require.NoError(t, err)
	require.Zero(t, resp.BaseResp.Code)
	require.Equal(t, sql.NullString{String: publishorigin.Heartbeat, Valid: true}, storedPublishOrigin(t, gdb))

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error { return tx.Exec(down).Error }))
	var columns int64
	require.NoError(t, gdb.Raw(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'raw_items' AND column_name = 'publish_origin'`).Scan(&columns).Error)
	require.Zero(t, columns)
	assertPublishRowCounts(t, gdb, 1)
}
