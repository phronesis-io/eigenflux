package recallsource

import (
	"context"
	"testing"

	"eigenflux_server/pkg/recall"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTwoTowerRecallSourceReadsRedisCandidates(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.Set("rec:two_tower_recall:active_version", "v1")
	mr.Set("rec:two_tower_recall:v1:user:1001:scored_candidates", "42:0.9500,77:0.8800,99:0.7700")

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		rdb.Close()
	})

	reader := recall.NewRedisRecallReader(rdb, "rec")
	source := NewTwoTowerRecallSource(reader, "", 3)
	got, err := source.Recall(context.Background(), "1001", 2)

	require.NoError(t, err)
	assert.Equal(t, []Candidate{
		{ItemID: 42, Score: 0.95, Source: TwoTower},
		{ItemID: 77, Score: 0.88, Source: TwoTower},
	}, got)
}

func TestTwoTowerRecallSourceReturnsEmptyForMissingUser(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.Set("rec:two_tower_recall:active_version", "v1")

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		rdb.Close()
	})

	reader := recall.NewRedisRecallReader(rdb, "rec")
	source := NewTwoTowerRecallSource(reader, "two_tower_recall", 50)
	got, err := source.Recall(context.Background(), "1001", 0)

	require.NoError(t, err)
	assert.Empty(t, got)
}
