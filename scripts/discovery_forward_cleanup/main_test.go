package main

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCleanupPreservesIdentityVersionsAndConcurrentWrites(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	raw := `{"agent_id":9223372036854775807,"active":true,"embedding":[1,0],"retrieval_slots":{"category":"design","taxonomy_version":"v1","lang":["en"],"provider_region":["US"]}}`
	key := "discovery:forward:agent:fixture:9223372036854775807:card"
	require.NoError(t, r.HSet(ctx, key, "version", "9223372036854775807", "data", raw).Err())
	unrelated := "discovery:forward:commission:fixture:1:statistics"
	require.NoError(t, r.HSet(ctx, unrelated, "data", raw).Err())
	n, err := cleanup(ctx, r, false)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, raw, r.HGet(ctx, key, "data").Val())
	n, err = cleanup(ctx, r, true)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	want := `{"agent_id":9223372036854775807,"active":true,"retrieval_slots":{"lang":["en"],"provider_region":["US"]}}`
	require.JSONEq(t, want, r.HGet(ctx, key, "data").Val())
	require.Contains(t, r.HGet(ctx, key, "data").Val(), `9223372036854775807`)
	require.Equal(t, "9223372036854775807", r.HGet(ctx, key, "version").Val())
	require.Equal(t, raw, r.HGet(ctx, unrelated, "data").Val())
	n, err = cleanup(ctx, r, true)
	require.NoError(t, err)
	require.Zero(t, n)
	n, err = replaceData.Run(ctx, r, []string{key}, raw, `{}`).Int()
	require.NoError(t, err)
	require.Zero(t, n)
	require.JSONEq(t, want, r.HGet(ctx, key, "data").Val())
	_, _, err = cleaned(`invalid`)
	require.Error(t, err)
}
