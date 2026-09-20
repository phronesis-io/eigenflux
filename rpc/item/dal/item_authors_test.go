package dal

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestBatchGetItemAuthors(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, gdb.Exec(`CREATE TABLE raw_items (item_id INTEGER PRIMARY KEY, author_agent_id INTEGER NOT NULL, raw_content TEXT NOT NULL, created_at INTEGER NOT NULL)`).Error)
	require.NoError(t, gdb.Exec(`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES (1, 100, 'a', 1), (2, 200, 'b', 1), (3, 100, 'c', 1)`).Error)

	empty, err := BatchGetItemAuthors(gdb, nil)
	require.NoError(t, err)
	require.Empty(t, empty)

	authors, err := BatchGetItemAuthors(gdb, []int64{1, 2, 3, 999})
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{1: 100, 2: 200, 3: 100}, authors)
	_, known := authors[999]
	require.False(t, known, "unknown item must be absent, not reported as author 0")
}
