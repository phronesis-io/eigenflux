package recall_test

import (
	"context"
	"testing"
	"time"

	"eigenflux_server/pkg/recall"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSurfaceHistoryOrdersByReportedTimeAndKeepsNewerDuplicate(t *testing.T) {
	rdb, _ := newRecallRedis(t)
	store := recall.NewSurfaceHistoryStore(rdb, "rec")
	now := time.Now()

	require.NoError(t, store.Upsert(context.Background(), []recall.SurfaceEvent{
		{AgentID: 42, ItemID: 100, ReportedAt: now.Add(-3 * time.Hour).UnixMilli()},
		{AgentID: 42, ItemID: 200, ReportedAt: now.Add(-time.Hour).UnixMilli()},
		{AgentID: 42, ItemID: 300, ReportedAt: now.Add(-2 * time.Hour).UnixMilli()},
	}))
	// A delayed retry must not move item 200 behind older events.
	require.NoError(t, store.Record(context.Background(), recall.SurfaceEvent{
		AgentID: 42, ItemID: 200, ReportedAt: now.Add(-4 * time.Hour).UnixMilli(),
	}))

	got, err := store.Recent(context.Background(), 42, 10)
	require.NoError(t, err)
	assert.Equal(t, []int64{200, 300, 100}, got)
}

func TestSurfaceHistoryPrunesWindowAndCapsLength(t *testing.T) {
	rdb, _ := newRecallRedis(t)
	store := recall.NewSurfaceHistoryStore(rdb, "rec")
	now := time.Now()
	events := []recall.SurfaceEvent{{
		AgentID: 42, ItemID: 1, ReportedAt: now.Add(-recall.SurfaceHistoryTTL - time.Hour).UnixMilli(),
	}}
	for i := int64(0); i < recall.SurfaceHistoryMaxItems+5; i++ {
		events = append(events, recall.SurfaceEvent{
			AgentID: 42, ItemID: 1000 + i, ReportedAt: now.Add(-time.Duration(recall.SurfaceHistoryMaxItems+5-i) * time.Millisecond).UnixMilli(),
		})
	}

	require.NoError(t, store.Upsert(context.Background(), events))
	cardinality, err := rdb.ZCard(context.Background(), recall.SurfaceHistoryKey("rec", 42)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(recall.SurfaceHistoryMaxItems), cardinality)

	got, err := store.Recent(context.Background(), 42, recall.SurfaceHistoryMaxItems+10)
	require.NoError(t, err)
	require.Len(t, got, recall.SurfaceHistoryMaxItems)
	assert.Equal(t, int64(1104), got[0])
	assert.NotContains(t, got, int64(1))
	assert.NotContains(t, got, int64(1000))
}

func TestSurfaceHistoryMissingAgentReturnsEmpty(t *testing.T) {
	rdb, _ := newRecallRedis(t)
	store := recall.NewSurfaceHistoryStore(rdb, "rec")

	got, err := store.Recent(context.Background(), 42, 20)

	require.NoError(t, err)
	assert.Empty(t, got)
}
