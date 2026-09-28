package featureindex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRegisteredViewsProjectOnlyOwnedFields(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	for _, d := range Definitions() {
		t.Run(d.Name, func(t *testing.T) {
			f := Forward{Redis: r, Namespace: d.Entity + ":test"}
			input := map[string]any{d.IDField: 7, d.VersionField: 9, "embedding": []float32{1, 0}, "unknown_future_field": "secret"}
			for _, field := range []string{"content", "summary", "search_text", "display_name", "title", "capability_description", "request_spec_text", "delivery_spec_text", "tags"} {
				input[field] = strings.Repeat("long-source-text", 1000)
			}
			require.NoError(t, f.Put(context.Background(), 7, d.Component, 9, input))
			stored := r.HGet(context.Background(), f.Key(7, d.Component), "data").Val()
			require.NotContains(t, stored, "long-source-text", "physical Redis value must exclude source text")
			rows, err := f.Get(context.Background(), []int64{7}, d.Component)
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(rows[7], &got))
			require.Len(t, got, 2)
			require.Error(t, f.Put(context.Background(), 8, d.Component, 9, input))
			require.Error(t, f.Put(context.Background(), 7, d.Component, 10, input))
			require.Error(t, f.Put(context.Background(), 7, "unregistered", 9, input))
		})
	}
	copy := Definitions()
	copy[0].Fields[0] = "poison"
	require.NotEqual(t, "poison", Definitions()[0].Fields[0])
}
func TestExpiredFeatureRetainsWriteFence(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	f := Forward{Redis: r, Namespace: "broadcast:v1"}
	require.NoError(t, f.Put(ctx, 7, "item", 10, map[string]any{"item_id": 7, "version": 10}))
	require.NoError(t, r.HSet(ctx, f.Key(7, "item"), "expires_at", time.Now().Add(-time.Second).UnixMilli()).Err())
	rows, err := f.Get(ctx, []int64{7}, "item")
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, f.Put(ctx, 7, "item", 9, map[string]any{"item_id": 7, "version": 9}))
	rows, err = f.Get(ctx, []int64{7}, "item")
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Equal(t, "10", r.HGet(ctx, f.Key(7, "item"), "version").Val())
	first, err := f.AllocateVersion(ctx)
	require.NoError(t, err)
	second, err := f.AllocateVersion(ctx)
	require.NoError(t, err)
	require.Greater(t, second, first)
}
