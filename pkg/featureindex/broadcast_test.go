package featureindex

import (
	"context"
	"eigenflux_server/pkg/agentutility"
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

func TestBroadcastUtilityProjectionKeepsEvidenceWithoutText(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	store := BroadcastIndex{Redis: r}
	ctx := context.Background()
	d := BroadcastDocument{ItemID: 8, Version: 1, Active: true, ContentHash: "digest", AgentUtility: agentutility.Classify("uvx mcp-server-git")}
	require.NoError(t, store.Write(ctx, d))
	rows, err := store.Read(ctx, []int64{8})
	require.NoError(t, err)
	require.Equal(t, d.AgentUtility, rows[8].AgentUtility)
	raw := r.HGet(ctx, store.Forward().Key(8, "item"), "data").Val()
	require.Contains(t, raw, agentutility.Version)
	require.NotContains(t, raw, "mcp-server-git")
	// Historical warm projections are readable without a new DB lookup and have
	// no authority to boost. The bounded periodic source loader fills evidence.
	d.ItemID = 9
	d.AgentUtility = agentutility.Evidence{}
	require.NoError(t, store.Write(ctx, d))
	rows, err = store.Read(ctx, []int64{9})
	require.NoError(t, err)
	require.Equal(t, agentutility.Unknown, rows[9].AgentUtility.Label())
}
