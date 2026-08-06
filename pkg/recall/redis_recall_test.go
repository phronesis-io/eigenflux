package recall

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRecallTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		rdb.Close()
	})
	return rdb, mr
}

func TestFetchUserScoredCandidates(t *testing.T) {
	rdb, mr := setupRecallTestRedis(t)
	mr.Set("rec:personalized_recall:active_version", "20260521T180000Z")
	mr.Set("rec:personalized_recall:20260521T180000Z:user:1001:scored_candidates", "42:0.9500,77:0.1234")

	reader := NewRedisRecallReader(rdb, "rec")
	got, err := reader.FetchUserScoredCandidates(context.Background(), "personalized_recall", "1001")

	require.NoError(t, err)
	assert.Equal(t, []ScoredCandidate{
		{ItemID: 42, Score: 0.95},
		{ItemID: 77, Score: 0.1234},
	}, got)
}

func TestFetchUserScoredCandidatesMissingUserReturnsEmpty(t *testing.T) {
	rdb, mr := setupRecallTestRedis(t)
	mr.Set("rec:personalized_recall:active_version", "v1")

	reader := NewRedisRecallReader(rdb, "rec")
	got, err := reader.FetchUserScoredCandidates(context.Background(), "personalized_recall", "1001")

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFetchUserScoredCandidatesRejectsMalformedValue(t *testing.T) {
	rdb, mr := setupRecallTestRedis(t)
	mr.Set("rec:personalized_recall:active_version", "v1")
	mr.Set("rec:personalized_recall:v1:user:1001:scored_candidates", "42:0.9,bad")

	reader := NewRedisRecallReader(rdb, "rec")
	_, err := reader.FetchUserScoredCandidates(context.Background(), "personalized_recall", "1001")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid candidate")
}

func TestFetchItemScoredNeighbors(t *testing.T) {
	rdb, mr := setupRecallTestRedis(t)
	mr.Set("rec:swing_i2i:active_version", "20260804T040000Z")
	mr.Set("rec:swing_i2i:20260804T040000Z:item:123:scored_neighbors", "456:1.0000,789:0.2346")

	reader := NewRedisRecallReader(rdb, "rec")
	got, err := reader.FetchItemScoredNeighbors(context.Background(), "swing_i2i", "123")

	require.NoError(t, err)
	assert.Equal(t, []ScoredCandidate{
		{ItemID: 456, Score: 1},
		{ItemID: 789, Score: 0.2346},
	}, got)
}

func TestFetchItemScoredNeighborsMissingSeedReturnsEmpty(t *testing.T) {
	rdb, mr := setupRecallTestRedis(t)
	mr.Set("rec:swing_i2i:active_version", "v1")

	reader := NewRedisRecallReader(rdb, "rec")
	got, err := reader.FetchItemScoredNeighbors(context.Background(), "swing_i2i", "123")

	require.NoError(t, err)
	assert.Empty(t, got)
}
