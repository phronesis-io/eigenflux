package agentindex

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
	d := Document{AgentID: 9, Version: 2, ProjectionVersion: 3, ActivityAt: 123, UpdatedAt: 456, Embedding: []float32{1, 0}, SearchText: "design", Active: true}
	fields := d.SearchFields()
	require.NotContains(t, fields, "activity_at")
	require.NotContains(t, fields, "updated_at")
	require.Contains(t, fields, "embedding")
	require.NoError(t, WriteForward(context.Background(), r, "agents-v1", d))
	rows, err := ReadForward(context.Background(), r, "agents-v1", []int64{9})
	require.NoError(t, err)
	require.Equal(t, d, rows[9])
	d.Active, d.ProjectionVersion, d.Version = false, 4, 3
	require.NoError(t, WriteForward(context.Background(), r, "agents-v1", d))
	d.Active, d.ProjectionVersion = true, 3
	require.NoError(t, WriteForward(context.Background(), r, "agents-v1", d))
	rows, err = ReadForward(context.Background(), r, "agents-v1", []int64{9})
	require.NoError(t, err)
	require.False(t, rows[9].Active)
}
