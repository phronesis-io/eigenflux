package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/item"
	"eigenflux_server/pkg/db"
	"eigenflux_server/rpc/item/dal"
	profiledal "eigenflux_server/rpc/profile/dal"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func publishPostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("PUBLISH_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PUBLISH_PG_TEST_DSN must name an isolated local pgc_publish_* database")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, u.Hostname())
	require.True(t, strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "pgc_publish_"))
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	schema := fmt.Sprintf("publish_%d", time.Now().UnixNano())
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	gdb, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	previous := db.DB
	db.DB = gdb
	t.Cleanup(func() {
		db.DB = previous
		sqlDB, _ := gdb.DB()
		require.NoError(t, sqlDB.Close())
		require.NoError(t, admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error)
		adminSQL, _ := admin.DB()
		require.NoError(t, adminSQL.Close())
	})
	require.NoError(t, gdb.AutoMigrate(&profiledal.Agent{}, &dal.RawItem{}, &dal.ProcessedItem{}))
	require.NoError(t, gdb.Exec(`CREATE TABLE item_stats (
		item_id BIGINT PRIMARY KEY REFERENCES raw_items(item_id),
		author_agent_id BIGINT NOT NULL REFERENCES agents(agent_id),
		consumed_count BIGINT NOT NULL DEFAULT 0, score_neg1_count BIGINT NOT NULL DEFAULT 0,
		score_0_count BIGINT NOT NULL DEFAULT 0, score_1_count BIGINT NOT NULL DEFAULT 0,
		score_2_count BIGINT NOT NULL DEFAULT 0, total_score BIGINT NOT NULL DEFAULT 0,
		created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL)`).Error)
	migration, err := os.ReadFile("../../migrations/000111_item_publish_outbox.sql")
	require.NoError(t, err)
	require.NoError(t, gdb.Exec(strings.Split(string(migration), "-- +goose Down")[0]).Error)
	seedPublishAuthor(t, gdb)
	return gdb
}

func TestPostgresPublishPersistenceFailureAndVisibility(t *testing.T) {
	for _, table := range []string{"raw_items", "processed_items", "item_stats", "item_publish_outbox"} {
		t.Run(table, func(t *testing.T) {
			gdb := publishPostgresDB(t)
			require.NoError(t, gdb.Exec(`CREATE FUNCTION reject_publish() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'injected publish persistence failure'; END; $$`).Error)
			require.NoError(t, gdb.Exec("CREATE TRIGGER reject_publish BEFORE INSERT ON "+table+
				" FOR EACH ROW EXECUTE FUNCTION reject_publish()").Error)
			resp, err := (&ItemServiceImpl{itemIDGen: publishFixtureID{}}).PublishItem(publishContext(), publishRequest())
			require.NoError(t, err)
			require.Equal(t, int32(500), resp.BaseResp.Code)
			assertPublishRowCounts(t, gdb, 0)
		})
	}
	t.Run("visibility", func(t *testing.T) {
		gdb := publishPostgresDB(t)
		// A separate pool cannot see raw/processed rows while the last write
		// is pending. The SQL reader uses the same schema without the writer's tx.
		observer, err := gorm.Open(postgres.Open(gdb.Dialector.(*postgres.Dialector).Config.DSN),
			&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, err)
		defer func() { sqlDB, _ := observer.DB(); require.NoError(t, sqlDB.Close()) }()
		checked := false
		require.NoError(t, gdb.Callback().Create().Before("gorm:create").Register("test:observe_publish", func(tx *gorm.DB) {
			if tx.Statement.Table == "item_stats" {
				assertPublishRowCounts(t, observer, 0)
				checked = true
			}
		}))
		svc := &ItemServiceImpl{itemIDGen: publishFixtureID{}}
		resp, err := svc.PublishItem(publishContext(), publishRequest())
		require.NoError(t, err)
		require.Zero(t, resp.BaseResp.Code)
		require.True(t, checked)
		assertPublishRowCounts(t, observer, 1)
		listing, err := svc.GetMyItems(context.Background(), &item.GetMyItemsReq{AuthorAgentId: 101})
		require.NoError(t, err)
		require.Len(t, listing.Items, 1)
		require.Equal(t, resp.ItemId, listing.Items[0].ItemId)
	})
}
