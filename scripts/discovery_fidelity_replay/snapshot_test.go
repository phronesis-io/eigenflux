package main

import (
	"context"
	"eigenflux_server/pkg/need"
	"encoding/json"
	"fmt"
	"sync"
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

type laneKeySource struct {
	*frozenSource
	mu       sync.Mutex
	contexts map[string]discovery.Context
}

func (s *laneKeySource) Recall(_ context.Context, c discovery.Context, k discovery.Kind, ch string, n int) ([]discovery.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contexts[channelKey(c, k, ch)] = c
	return nil, nil
}
func (s *laneKeySource) Hydrate(_ context.Context, _ int64, _ discovery.Mode, docs []discovery.Document) ([]discovery.Document, error) {
	return docs, nil
}

func TestChannelKeySeparatesNeedDenseLanesAndRetainsLegacyKeys(t *testing.T) {
	const now = int64(1800000000000)
	c := discovery.Context{ID: 1, OwnerID: 1, State: "active", Kinds: []discovery.Kind{discovery.Broadcast}, Vector: []float32{1, 0}, CapturedNeed: &need.Snapshot{}}
	require.Equal(t, "1:broadcast:dense", channelKey(c, discovery.Broadcast, "dense"))
	s := &laneKeySource{frozenSource: newFrozenSource(&snapshot{}), contexts: map[string]discovery.Context{}}
	rules := discovery.Rules{}
	for _, k := range discovery.AllKinds {
		rules[k] = map[discovery.Mode]discovery.Rule{}
		for _, m := range []discovery.Mode{discovery.Search, discovery.Recommendation} {
			rules[k][m] = discovery.Rule{Version: "test", BM25Scale: 1, HalfLifeMS: 1000}
		}
	}
	ids := localIDs(100)
	e := discovery.Engine{Sources: s, IDs: &ids, Rules: rules, NeedRecallBPS: 10000}
	_, err := e.ReplayContexts(context.Background(), 1, []discovery.Context{c}, c.Kinds, 20, now)
	require.NoError(t, err)
	freshKey := fmt.Sprintf("1:broadcast:dense:since=%d", now-7*24*60*60*1000)
	require.Contains(t, s.contexts, "1:broadcast:dense")
	require.Contains(t, s.contexts, freshKey)
	require.Equal(t, now-7*24*60*60*1000, s.contexts[freshKey].CreatedSince())
	snap := snapshot{Channels: map[string]channel{
		"1:broadcast:dense": {Limit: 80, Documents: storeDocs([]discovery.Document{{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 80}}})},
		freshKey:            {Limit: 40, Documents: storeDocs([]discovery.Document{{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 40}}})},
	}}
	raw, err := json.Marshal(snap)
	require.NoError(t, err)
	var decoded snapshot
	require.NoError(t, json.Unmarshal(raw, &decoded))
	frozen := newFrozenSource(&decoded)
	for key, limit := range map[string]int{"1:broadcast:dense": 80, freshKey: 40} {
		docs, err := frozen.Recall(context.Background(), s.contexts[key], discovery.Broadcast, "dense", limit)
		require.NoError(t, err)
		require.Equal(t, int64(limit), docs[0].Ref.ID)
	}
}
