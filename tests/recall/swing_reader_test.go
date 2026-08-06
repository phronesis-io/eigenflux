package recall_test

import (
	"context"
	"testing"

	"eigenflux_server/pkg/recall"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchItemScoredNeighbors(t *testing.T) {
	rdb, mr := newRecallRedis(t)
	mr.Set("rec:swing_i2i:active_version", "20260804T040000Z")
	mr.Set("rec:swing_i2i:20260804T040000Z:item:123:scored_neighbors", "456:1.0000,789:0.2346")

	reader := recall.NewRedisRecallReader(rdb, "rec")
	got, err := reader.FetchItemScoredNeighbors(context.Background(), "swing_i2i", "123")

	require.NoError(t, err)
	assert.Equal(t, []recall.ScoredCandidate{
		{ItemID: 456, Score: 1},
		{ItemID: 789, Score: 0.2346},
	}, got)
}

func TestFetchItemScoredNeighborsMissingSeedReturnsEmpty(t *testing.T) {
	rdb, mr := newRecallRedis(t)
	mr.Set("rec:swing_i2i:active_version", "v1")

	reader := recall.NewRedisRecallReader(rdb, "rec")
	got, err := reader.FetchItemScoredNeighbors(context.Background(), "swing_i2i", "123")

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFetchItemScoredNeighborsBatchAllowsMissingSeeds(t *testing.T) {
	rdb, mr := newRecallRedis(t)
	mr.Set("rec:swing_i2i:active_version", "v1")
	mr.Set("rec:swing_i2i:v1:item:123:scored_neighbors", "456:0.7500")

	reader := recall.NewRedisRecallReader(rdb, "rec")
	got, err := reader.FetchItemScoredNeighborsBatch(context.Background(), "swing_i2i", []int64{123, 999})

	require.NoError(t, err)
	assert.Equal(t, []recall.ScoredCandidate{{ItemID: 456, Score: 0.75}}, got[123])
	assert.Empty(t, got[999])
}

func TestFetchItemScoredNeighborsRejectsMalformedValue(t *testing.T) {
	rdb, mr := newRecallRedis(t)
	mr.Set("rec:swing_i2i:active_version", "v1")
	mr.Set("rec:swing_i2i:v1:item:123:scored_neighbors", "456:1.0,bad")

	reader := recall.NewRedisRecallReader(rdb, "rec")
	_, err := reader.FetchItemScoredNeighbors(context.Background(), "swing_i2i", "123")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid candidate")
}

func newRecallRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return rdb, mr
}
