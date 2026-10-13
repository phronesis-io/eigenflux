package featureindex

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAgentSearchProjectionExcludesRankingFeatures(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	d := AgentDocument{AgentID: 9, Version: 2, ProjectionVersion: 3, ActivityAt: 123, UpdatedAt: 456, Embedding: []float32{1, 0}, SearchText: "design", Active: true}
	fields := d.SearchFields()
	require.NotContains(t, fields, "activity_at")
	require.NotContains(t, fields, "updated_at")
	require.Contains(t, fields, "embedding")
	require.NoError(t, (AgentIndex{Redis: r, IndexName: "agents-v1"}).Write(context.Background(), d))
	rows, err := (AgentIndex{Redis: r, IndexName: "agents-v1"}).Read(context.Background(), []int64{9})
	require.NoError(t, err)
	expected := d
	expected.Embedding = nil
	expected.SearchText = ""
	require.Equal(t, expected, rows[9])
	require.NotContains(t, r.HGet(context.Background(), (AgentIndex{Redis: r, IndexName: "agents-v1"}).Forward().Key(9, "card"), "data").Val(), `"embedding"`)
	require.Equal(t, []float32{1, 0}, d.SearchFields()["embedding"], "forward write must not mutate ES vector")
	d.Active, d.ProjectionVersion, d.Version = false, 4, 3
	require.NoError(t, (AgentIndex{Redis: r, IndexName: "agents-v1"}).Write(context.Background(), d))
	d.Active, d.ProjectionVersion = true, 3
	require.NoError(t, (AgentIndex{Redis: r, IndexName: "agents-v1"}).Write(context.Background(), d))
	rows, err = (AgentIndex{Redis: r, IndexName: "agents-v1"}).Read(context.Background(), []int64{9})
	require.NoError(t, err)
	require.False(t, rows[9].Active)
}
