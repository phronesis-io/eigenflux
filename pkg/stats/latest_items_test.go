package stats

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPushLatestItemInterleavesTypes(t *testing.T) {
	ctx := context.Background()
	rdb := newMiniredisClient(t)

	inputs := []*ItemSnapshot{
		{ID: 1, Agent: "Info-1", Type: "info", Content: "info-1"},
		{ID: 2, Agent: "Info-2", Type: "info", Content: "info-2"},
		{ID: 3, Agent: "Demand-1", Type: "demand", Content: "demand-1"},
		{ID: 4, Agent: "Info-3", Type: "info", Content: "info-3"},
		{ID: 5, Agent: "Supply-1", Type: "supply", Content: "supply-1"},
		{ID: 6, Agent: "Info-4", Type: "info", Content: "info-4"},
	}

	for _, item := range inputs {
		require.NoError(t, PushLatestItem(ctx, rdb, item))
	}

	items, err := GetLatestItems(ctx, rdb, 10)
	require.NoError(t, err)
	require.Len(t, items, 6)

	assert.Equal(t, []int64{3, 5, 6, 4, 2, 1}, itemIDs(items))
	assert.Equal(t, []string{"demand", "supply", "info", "info", "info", "info"}, itemTypes(items))

	length, err := rdb.LLen(ctx, KeyLatestItems).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(6), length)
}

func TestGetLatestItemsReadsLegacyMainList(t *testing.T) {
	ctx := context.Background()
	rdb := newMiniredisClient(t)

	legacyItem := `{"id":42,"agent":"Legacy","country":"US","type":"request","domains":["tech"],"content":"legacy","url":"","notes":{}}`
	require.NoError(t, rdb.LPush(ctx, KeyLatestItems, legacyItem).Err())

	items, err := GetLatestItems(ctx, rdb, 10)
	require.NoError(t, err)
	require.Len(t, items, 1)

	assert.Equal(t, int64(42), items[0].ID)
	assert.Equal(t, "request", items[0].Type)
	assert.Equal(t, "Legacy", items[0].Agent)
}

func TestClearLatestItemsRemovesTypeBuckets(t *testing.T) {
	ctx := context.Background()
	rdb := newMiniredisClient(t)

	require.NoError(t, PushLatestItem(ctx, rdb, &ItemSnapshot{ID: 1, Agent: "Demand", Type: "demand", Content: "demand"}))
	require.NoError(t, PushLatestItem(ctx, rdb, &ItemSnapshot{ID: 2, Agent: "Info", Type: "info", Content: "info"}))

	require.NoError(t, ClearLatestItems(ctx, rdb))

	exists, err := rdb.Exists(ctx, KeyLatestItems, KeyLatestItemTypes, latestItemsTypeKey("demand"), latestItemsTypeKey("info")).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), exists)
}

func newMiniredisClient(t *testing.T) *redis.Client {
	t.Helper()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
		server.Close()
	})

	return client
}

func itemIDs(items []*ItemSnapshot) []int64 {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func itemTypes(items []*ItemSnapshot) []string {
	types := make([]string, 0, len(items))
	for _, item := range items {
		types = append(types, item.Type)
	}
	return types
}
