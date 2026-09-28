package featureindex

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestWarmReadDoesNotNeedSourceAndMissingIsNotEmpty(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	f := (BroadcastIndex{Redis: r}).Forward()
	s := BroadcastIndex{Redis: r}
	d := BroadcastDocument{ItemID: 7, Version: 10, QualityScore: .7, Active: true, ContentHash: "content-digest"}
	require.NoError(t, f.Put(ctx, 7, "item", 10, d))
	rows, err := s.Read(ctx, []int64{7})
	require.NoError(t, err)
	require.Equal(t, d, rows[7])
	require.NotContains(t, r.HGet(ctx, f.Key(7, "item"), "data").Val(), "embedding")
	_, err = s.Read(ctx, []int64{8})
	require.Error(t, err, "a missing source must not fabricate features")
	require.NoError(t, r.HSet(ctx, f.Key(7, "item"), "expires_at", time.Now().Add(-time.Second).UnixMilli()).Err())
	_, err = s.Read(ctx, []int64{7})
	require.Error(t, err, "expired features require source repair")
	d.Active = false
	d.Version = 11
	require.NoError(t, f.Put(ctx, 7, "item", 11, d))
	rows, err = s.Read(ctx, []int64{7})
	require.NoError(t, err)
	require.False(t, rows[7].Active)
}
