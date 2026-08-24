package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCountPositiveFeedback(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.Exec(`
		CREATE TABLE item_stats (
			item_id INTEGER PRIMARY KEY,
			score_1_count INTEGER NOT NULL DEFAULT 0,
			score_2_count INTEGER NOT NULL DEFAULT 0
		)
	`).Error)

	count, err := countPositiveFeedback(database)
	require.NoError(t, err)
	require.Equal(t, int64(0), count)

	require.NoError(t, database.Exec(`
		INSERT INTO item_stats (item_id, score_1_count, score_2_count)
		VALUES (1, 2, 3), (2, 0, 1), (3, 0, 0)
	`).Error)

	count, err = countPositiveFeedback(database)
	require.NoError(t, err)
	require.Equal(t, int64(6), count)
}
