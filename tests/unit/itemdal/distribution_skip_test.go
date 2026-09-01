package itemdal_test

import (
	"testing"

	"eigenflux_server/rpc/item/dal"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func distributionSkipTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
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

func TestFindPriorExactBroadcastInGroupRequiresSameAuthorAndContent(t *testing.T) {
	database := distributionSkipTestDB(t)
	require.NoError(t, database.Exec(`
		INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES
			(10, 1, 'exact repeated content', 1000),
			(11, 2, 'other author content', 2000),
			(12, 1, 'exact repeated content', 3000),
			(13, 1, 'related but distinct update', 4000);
		INSERT INTO processed_items (item_id, status, summary, group_id) VALUES
			(10, 3, '  Earlier   broadcast  ', 99),
			(11, 3, 'Other broadcast', 99),
			(12, 0, '', NULL),
			(13, 0, '', NULL);
	`).Error)

	ref, err := dal.FindPriorExactBroadcastInGroup(database, 1, 99, 12, "exact repeated content")
	require.NoError(t, err)
	require.NotNil(t, ref)
	require.Equal(t, int64(10), ref.ItemID)
	require.Equal(t, int64(1000), ref.CreatedAt)
	require.Equal(t, "Earlier broadcast", ref.Title)

	wrongAuthor, err := dal.FindPriorExactBroadcastInGroup(database, 3, 99, 12, "exact repeated content")
	require.NoError(t, err)
	require.Nil(t, wrongAuthor)

	relatedOnly, err := dal.FindPriorExactBroadcastInGroup(database, 1, 99, 13, "related but distinct update")
	require.NoError(t, err)
	require.Nil(t, relatedOnly, "a merely related same-author item must not become an exact-duplicate reference")
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
