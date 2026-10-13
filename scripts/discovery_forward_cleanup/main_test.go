package main

import (
	"context"
	"eigenflux_server/pkg/featureindex"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"encoding/json"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
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

func TestCleanupRemovesTextAndPreservesBroadcastEvidence(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	for _, kind := range []string{"broadcast:v1:7:item", "agent:v1:7:card", "commission:v1:7:catalogue"} {
		key := "discovery:forward:" + kind
		payload := map[string]any{"active": true, "content": "正文", "summary": "摘要", "lang": "zh", "retrieval_slots": map[string]any{}, "title": "title", "search_text": "full description", "capability_description": "large", "request_spec_text": "request", "delivery_spec_text": "delivery", "display_name": "name", "tags": []string{"tag"}, "quality_score": .8}
		switch kind {
		case "broadcast:v1:7:item":
			payload["item_id"], payload["author_id"], payload["version"] = 7, 8, 123
		case "agent:v1:7:card":
			payload["agent_id"], payload["projection_version"] = 7, 123
		case "commission:v1:7:catalogue":
			payload["commission_id"], payload["catalogue_version"] = 7, 123
		}
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		require.NoError(t, r.HSet(ctx, key, "data", raw, "version", "123", "expires_at", "456").Err())
		require.NoError(t, r.Expire(ctx, key, time.Hour).Err())
	}
	n, err := cleanup(ctx, r, true)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	for _, kind := range []string{"broadcast:v1:7:item", "agent:v1:7:card", "commission:v1:7:catalogue"} {
		key := "discovery:forward:" + kind
		var doc map[string]any
		require.NoError(t, json.Unmarshal([]byte(r.HGet(ctx, key, "data").Val()), &doc))
		for _, field := range []string{"content", "summary", "title", "search_text", "capability_description", "request_spec_text", "delivery_spec_text", "display_name", "tags"} {
			require.NotContains(t, doc, field)
		}
		require.Equal(t, .8, doc["quality_score"])
		if kind == "broadcast:v1:7:item" {
			require.Equal(t, featureindex.BroadcastContentHash(7, 8, "正文\n摘要", searchindex.Slots{Lang: []string{"zh"}}), doc["content_hash"])
		} else {
			require.NotContains(t, doc, "content_hash")
		}
		require.Equal(t, "123", r.HGet(ctx, key, "version").Val())
		require.Equal(t, "456", r.HGet(ctx, key, "expires_at").Val())
		require.Equal(t, time.Hour, r.TTL(ctx, key).Val())
	}
	n, err = cleanup(ctx, r, true)
	require.NoError(t, err)
	require.Zero(t, n)
	key := "discovery:forward:broadcast:v1:7:item"
	old := r.HGet(ctx, key, "data").Val()
	require.NoError(t, r.Del(ctx, key).Err())
	n, err = replaceData.Run(ctx, r, []string{key}, old, `{}`).Int()
	require.NoError(t, err)
	require.Zero(t, n)
	require.Zero(t, r.Exists(ctx, key).Val(), "cleanup must not resurrect removed keys")
}
