package recall_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"eigenflux_server/pkg/impr"
	"eigenflux_server/pkg/recall"
	"eigenflux_server/pkg/recallsource"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSwingI2IRecallAggregatesSeedsAndFiltersSeenItems(t *testing.T) {
	rdb, mr := newRecallRedis(t)
	ctx := context.Background()
	mr.Set("rec:swing_i2i:active_version", "v1")
	mr.Set("rec:swing_i2i:v1:item:300:scored_neighbors", "900:0.6000,800:0.7000,200:1.0000")
	mr.Set("rec:swing_i2i:v1:item:200:scored_neighbors", "900:0.5000,700:0.4000")
	mr.Set("rec:swing_i2i:v1:item:100:scored_neighbors", "999:1.0000")
	_, err := mr.SAdd(fmt.Sprintf(impr.KeyItemIDs, 42), "100", "200", "300")
	require.NoError(t, err)

	reader := recall.NewRedisRecallReader(rdb, "rec")
	surfaceHistory := recall.NewSurfaceHistoryStore(rdb, "rec")
	now := time.Now()
	require.NoError(t, surfaceHistory.Upsert(ctx, []recall.SurfaceEvent{
		{AgentID: 42, ItemID: 100, ReportedAt: now.Add(-3 * time.Hour).UnixMilli()},
		{AgentID: 42, ItemID: 200, ReportedAt: now.Add(-2 * time.Hour).UnixMilli()},
		{AgentID: 42, ItemID: 300, ReportedAt: now.Add(-time.Hour).UnixMilli()},
	}))
	source := recallsource.NewSwingI2IRecallSource(reader, surfaceHistory, rdb, 2, 10)
	got, err := source.Recall(ctx, "42", 0)

	require.NoError(t, err)
	assert.Equal(t, []recallsource.Candidate{
		{ItemID: 900, Score: 1.1, Source: recallsource.SwingI2I},
		{ItemID: 800, Score: 0.7, Source: recallsource.SwingI2I},
		{ItemID: 700, Score: 0.4, Source: recallsource.SwingI2I},
	}, got)
}

func TestSwingI2IRecallAppliesRequestLimitAndStableTieBreak(t *testing.T) {
	rdb, mr := newRecallRedis(t)
	mr.Set("rec:swing_i2i:active_version", "v1")
	mr.Set("rec:swing_i2i:v1:item:300:scored_neighbors", "700:0.5000,800:0.5000,600:0.4000")
	_, err := mr.SAdd(fmt.Sprintf(impr.KeyItemIDs, 42), "300")
	require.NoError(t, err)

	reader := recall.NewRedisRecallReader(rdb, "rec")
	surfaceHistory := recall.NewSurfaceHistoryStore(rdb, "rec")
	require.NoError(t, surfaceHistory.Record(context.Background(), recall.SurfaceEvent{
		AgentID: 42, ItemID: 300, ReportedAt: time.Now().UnixMilli(),
	}))
	source := recallsource.NewSwingI2IRecallSource(reader, surfaceHistory, rdb, 20, 100)
	got, err := source.Recall(context.Background(), "42", 1)

	require.NoError(t, err)
	assert.Equal(t, []recallsource.Candidate{
		{ItemID: 800, Score: 0.5, Source: recallsource.SwingI2I},
	}, got)
}

func TestSwingI2IRecallDoesNotFallBackToImpressions(t *testing.T) {
	rdb, mr := newRecallRedis(t)
	_, err := mr.SAdd(fmt.Sprintf(impr.KeyItemIDs, 42), "300")
	require.NoError(t, err)
	reader := recall.NewRedisRecallReader(rdb, "rec")
	surfaceHistory := recall.NewSurfaceHistoryStore(rdb, "rec")
	source := recallsource.NewSwingI2IRecallSource(reader, surfaceHistory, rdb, 20, 100)

	got, err := source.Recall(context.Background(), "42", 0)

	require.NoError(t, err)
	assert.Empty(t, got)
}
