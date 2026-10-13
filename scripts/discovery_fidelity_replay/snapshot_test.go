package main

import (
	"context"
	"encoding/json"
	"testing"

	"eigenflux_server/rpc/sort/discovery"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
	"github.com/stretchr/testify/require"
)

func TestPrivateSnapshotPreservesContextSpecificExclusionEvidence(t *testing.T) {
	a := discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 1}, Text: "private source exclusion evidence", NeedExclusionText: true}
	b := a
	b.Text = "summary only"
	b.NeedExclusionText = false
	s := snapshot{Hydration: []hydrationBatch{{storeDocs([]discovery.Document{a}), storeDocs([]discovery.Document{a})}, {storeDocs([]discovery.Document{b}), storeDocs([]discovery.Document{b})}}}
	data, err := json.Marshal(s)
	require.NoError(t, err)
	var decoded snapshot
	require.NoError(t, json.Unmarshal(data, &decoded))
	source := newFrozenSource(&decoded)
	for _, d := range []discovery.Document{a, b} {
		got, err := source.Hydrate(context.Background(), 1, discovery.Recommendation, []discovery.Document{d})
		require.NoError(t, err)
		require.Equal(t, []discovery.Document{d}, got)
	}
	_, err = source.Hydrate(context.Background(), 1, discovery.Recommendation, nil)
	require.Error(t, err)
	source = newFrozenSource(&decoded)
	_, err = source.Hydrate(context.Background(), 1, discovery.Recommendation, []discovery.Document{b})
	require.ErrorContains(t, err, "differs")
}
func TestReadOnlyRedisAllowsHandshakeAndReadsButRejectsWrites(t *testing.T) {
	server := miniredis.RunT(t)
	server.Set("item", "value")
	c := redis.NewClient(&redis.Options{Addr: server.Addr(), Protocol: 2, MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}})
	defer c.Close()
	c.AddHook(readOnlyRedis{})
	ctx := context.Background()
	got, err := c.Get(ctx, "item").Result()
	require.NoError(t, err)
	require.Equal(t, "value", got)
	require.ErrorContains(t, c.Set(ctx, "item", "changed", 0).Err(), "denied")
	p := c.TxPipeline()
	p.ZRemRangeByScore(ctx, "history", "-inf", "0")
	_, err = p.Exec(ctx)
	require.ErrorContains(t, err, "denied")
	got, err = server.Get("item")
	require.NoError(t, err)
	require.Equal(t, "value", got)
}
