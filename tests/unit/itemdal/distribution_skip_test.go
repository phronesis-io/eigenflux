package itemdal_test

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"

	"eigenflux_server/rpc/item/dal"

	"github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var registerMD5SQLiteDriver sync.Once

const (
	dayMillis           = int64(24 * 60 * 60 * 1000)
	forcedCollisionHash = "00000000000000000000000000000000"
)

func distributionSkipTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	registerMD5SQLiteDriver.Do(func() {
		sql.Register("sqlite3_with_md5", &sqlite3.SQLiteDriver{
			ConnectHook: func(conn *sqlite3.SQLiteConn) error {
				return conn.RegisterFunc("md5", func(value string) string {
					if value == "collision-a" || value == "collision-b" {
						return forcedCollisionHash
					}
					digest := md5.Sum([]byte(value))
					return hex.EncodeToString(digest[:])
				}, true)
			},
		})
	})
	dsn := fmt.Sprintf("file:%x?mode=memory&cache=shared", md5.Sum([]byte(t.Name())))
	database, err := gorm.Open(sqlite.New(sqlite.Config{DriverName: "sqlite3_with_md5", DSN: dsn}), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.Exec(`
		CREATE TABLE raw_items (
			item_id INTEGER PRIMARY KEY,
			author_agent_id INTEGER NOT NULL,
			raw_content TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
	`).Error)
	require.NoError(t, database.Exec(`
		CREATE TABLE processed_items (
			item_id INTEGER PRIMARY KEY,
			status INTEGER NOT NULL DEFAULT 0,
			distribution_skip_reason TEXT NOT NULL DEFAULT '',
			duplicate_of_item_id INTEGER NULL,
			summary TEXT,
			group_id INTEGER,
			updated_at INTEGER NOT NULL DEFAULT 0
		);
	`).Error)
	return database
}

func TestFindPriorExactBroadcastRequiresSameAuthorAndContentAcrossGroups(t *testing.T) {
	database := distributionSkipTestDB(t)
	currentCreatedAt := int64(40 * dayMillis)
	require.NoError(t, database.Exec(`
		INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES
			(10, 1, 'exact repeated content', ?),
			(11, 2, 'exact repeated content', ?),
			(12, 1, 'exact repeated content', ?),
			(13, 1, 'related but distinct update', ?);
		INSERT INTO processed_items (item_id, status, summary, group_id) VALUES
			(10, 3, '  Earlier   broadcast  ', 41),
			(11, 3, 'Other broadcast', 99),
			(12, 0, '', NULL),
			(13, 0, '', NULL);
	`, currentCreatedAt-dayMillis, currentCreatedAt-dayMillis, currentCreatedAt, currentCreatedAt+dayMillis).Error)

	contentHash := md5Hex("exact repeated content")

	ref, err := dal.FindPriorExactBroadcast(database, 1, 12, currentCreatedAt, contentHash, "exact repeated content")
	require.NoError(t, err)
	require.NotNil(t, ref)
	require.Equal(t, int64(10), ref.ItemID)
	require.Equal(t, currentCreatedAt-dayMillis, ref.CreatedAt)
	require.Equal(t, "Earlier broadcast", ref.Title)

	wrongAuthor, err := dal.FindPriorExactBroadcast(database, 3, 12, currentCreatedAt, contentHash, "exact repeated content")
	require.NoError(t, err)
	require.Nil(t, wrongAuthor)

	legacyRef, err := dal.FindPriorExactBroadcastInGroup(database, 1, 41, 12, "exact repeated content")
	require.NoError(t, err)
	require.NotNil(t, legacyRef)
	require.Equal(t, int64(10), legacyRef.ItemID)
}

func TestFindPriorExactBroadcastRejectsHashCollision(t *testing.T) {
	database := distributionSkipTestDB(t)
	require.NoError(t, database.Exec(`
		INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES
			(20, 1, 'collision-a', 1000),
			(21, 1, 'collision-b', 2000);
		INSERT INTO processed_items (item_id, status, summary, group_id) VALUES
			(20, 3, 'Hash collision candidate', 1),
			(21, 0, 'Current', 2);
	`).Error)

	ref, err := dal.FindPriorExactBroadcast(database, 1, 21, 2000, forcedCollisionHash, "collision-b")
	require.NoError(t, err)
	require.Nil(t, ref, "byte-distinct content must survive a hash collision")
}

func TestFindPriorExactBroadcastWindowStatusSelfAndOrdering(t *testing.T) {
	t.Run("includes exact 30-day boundary", func(t *testing.T) {
		database := distributionSkipTestDB(t)
		currentCreatedAt := int64(40 * dayMillis)
		require.NoError(t, database.Exec(`
			INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES
				(30, 1, 'repeat', ?), (31, 1, 'repeat', ?);
			INSERT INTO processed_items (item_id, status) VALUES (30, 3), (31, 0);
		`, currentCreatedAt-30*dayMillis, currentCreatedAt).Error)

		ref, err := dal.FindPriorExactBroadcast(database, 1, 31, currentCreatedAt, md5Hex("repeat"), "repeat")
		require.NoError(t, err)
		require.NotNil(t, ref)
		require.Equal(t, int64(30), ref.ItemID)
	})

	t.Run("excludes content older than 30 days", func(t *testing.T) {
		database := distributionSkipTestDB(t)
		currentCreatedAt := int64(40 * dayMillis)
		require.NoError(t, database.Exec(`
			INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES
				(40, 1, 'repeat', ?), (41, 1, 'repeat', ?);
			INSERT INTO processed_items (item_id, status) VALUES (40, 3), (41, 0);
		`, currentCreatedAt-30*dayMillis-1, currentCreatedAt).Error)

		ref, err := dal.FindPriorExactBroadcast(database, 1, 41, currentCreatedAt, md5Hex("repeat"), "repeat")
		require.NoError(t, err)
		require.Nil(t, ref)
	})

	t.Run("excludes unfinished candidate", func(t *testing.T) {
		database := distributionSkipTestDB(t)
		require.NoError(t, database.Exec(`
			INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES
				(50, 1, 'repeat', 1000), (51, 1, 'repeat', 2000);
			INSERT INTO processed_items (item_id, status) VALUES (50, 2), (51, 0);
		`).Error)

		ref, err := dal.FindPriorExactBroadcast(database, 1, 51, 2000, md5Hex("repeat"), "repeat")
		require.NoError(t, err)
		require.Nil(t, ref)
	})

	t.Run("excludes current item", func(t *testing.T) {
		database := distributionSkipTestDB(t)
		require.NoError(t, database.Exec(`
			INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at)
			VALUES (60, 1, 'repeat', 1000);
			INSERT INTO processed_items (item_id, status) VALUES (60, 3);
		`).Error)

		ref, err := dal.FindPriorExactBroadcast(database, 1, 60, 1000, md5Hex("repeat"), "repeat")
		require.NoError(t, err)
		require.Nil(t, ref)
	})

	t.Run("orders equal timestamps by item ID", func(t *testing.T) {
		database := distributionSkipTestDB(t)
		require.NoError(t, database.Exec(`
			INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES
				(70, 1, 'repeat', 1000), (71, 1, 'repeat', 1000),
				(72, 1, 'repeat', 1000), (73, 1, 'repeat', 1001);
			INSERT INTO processed_items (item_id, status) VALUES
				(70, 3), (71, 0), (72, 3), (73, 3);
		`).Error)

		ref, err := dal.FindPriorExactBroadcast(database, 1, 71, 1000, md5Hex("repeat"), "repeat")
		require.NoError(t, err)
		require.NotNil(t, ref)
		require.Equal(t, int64(70), ref.ItemID)
	})
}

func TestFindPriorExactBroadcastFailsOpenOnQueryError(t *testing.T) {
	database := distributionSkipTestDB(t)
	require.NoError(t, database.Exec("DROP TABLE processed_items").Error)

	ref, err := dal.FindPriorExactBroadcast(database, 1, 2, 2000, md5Hex("repeat"), "repeat")
	require.Error(t, err)
	require.Nil(t, ref)
}

func md5Hex(value string) string {
	digest := md5.Sum([]byte(value))
	return hex.EncodeToString(digest[:])
}

func TestMarkItemDistributionSkippedPersistsDuplicateReference(t *testing.T) {
	database := distributionSkipTestDB(t)
	require.NoError(t, database.Exec(`INSERT INTO processed_items (item_id, status) VALUES (12, 1)`).Error)

	duplicateOf := int64(10)
	require.NoError(t, dal.MarkItemDistributionSkipped(database, 12, dal.DistributionSkipDuplicate, &duplicateOf))

	metadata, err := dal.BatchGetDistributionSkipMetadata(database, []int64{12})
	require.NoError(t, err)
	require.Equal(t, dal.StatusDiscarded, metadata[12].Status)
	require.Equal(t, dal.DistributionSkipDuplicate, metadata[12].DistributionSkipReason)
	require.NotNil(t, metadata[12].DuplicateOfItemID)
	require.Equal(t, duplicateOf, *metadata[12].DuplicateOfItemID)
}
