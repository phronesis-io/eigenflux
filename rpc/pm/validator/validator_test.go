package validator

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	itemdal "eigenflux_server/rpc/item/dal"
)

func TestValidateItemAvailable(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, gdb.Exec(`CREATE TABLE processed_items (item_id INTEGER PRIMARY KEY, status INTEGER NOT NULL DEFAULT 0)`).Error)
	require.NoError(t, gdb.Exec(`INSERT INTO processed_items (item_id, status) VALUES (1, ?), (2, ?), (3, ?), (4, ?), (5, ?), (6, ?)`,
		itemdal.StatusCompleted, itemdal.StatusDeleted, itemdal.StatusPending, itemdal.StatusProcessing, itemdal.StatusFailed, itemdal.StatusDiscarded).Error)

	v := NewValidator(gdb, nil)
	for _, tc := range []struct {
		name   string
		itemID int64
	}{
		{"completed", 1},
		{"pending", 3},
		{"processing", 4},
		{"failed", 5},
	} {
		t.Run("reachable "+tc.name, func(t *testing.T) {
			require.NoError(t, v.ValidateItemAvailable(context.Background(), tc.itemID))
		})
	}
	for _, tc := range []struct {
		name   string
		itemID int64
	}{
		{"deleted", 2},
		{"discarded", 6},
	} {
		t.Run("unavailable "+tc.name, func(t *testing.T) {
			err := v.ValidateItemAvailable(context.Background(), tc.itemID)
			require.True(t, errors.Is(err, ErrItemNotAvailable), "err = %v", err)
		})
	}

	t.Run("missing processing record is an error, not unavailable", func(t *testing.T) {
		err := v.ValidateItemAvailable(context.Background(), 7)
		require.Error(t, err)
		require.False(t, errors.Is(err, ErrItemNotAvailable))
	})

	t.Run("lookup failure is not reported as unavailable", func(t *testing.T) {
		require.NoError(t, gdb.Exec(`DROP TABLE processed_items`).Error)
		err := v.ValidateItemAvailable(context.Background(), 1)
		require.Error(t, err)
		require.False(t, errors.Is(err, ErrItemNotAvailable))
	})
}
