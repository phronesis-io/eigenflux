package featureindex

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestForwardVersionPrecisionAndGenerationIsolation(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	f := Forward{Redis: r, Namespace: "agent:v1"}
	ctx := context.Background()
	const id = int64(9223372036854775807)
	require.NoError(t, f.Put(ctx, id, "card", 9007199254740993, map[string]any{"agent_id": id, "projection_version": int64(9007199254740993), "activity_at": 123}))
	require.NoError(t, f.Put(ctx, id, "card", 9007199254740992, map[string]any{"agent_id": id, "projection_version": int64(9007199254740992), "activity_at": 100}))
	rows, err := f.Get(ctx, []int64{id, 1}, "card")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.JSONEq(t, `{"agent_id":9223372036854775807,"projection_version":9007199254740993,"activity_at":123}`, string(rows[id]))
	require.Equal(t, 168*time.Hour, r.TTL(ctx, f.Key(id, "card")).Val())
	other := Forward{Redis: r, Namespace: "agent:v2"}
	require.NoError(t, other.Put(ctx, id, "card", 1, map[string]any{"agent_id": id, "projection_version": 1, "activity_at": 1}))
	rows, err = f.Get(ctx, []int64{id}, "card")
	require.NoError(t, err)
	require.JSONEq(t, `{"agent_id":9223372036854775807,"projection_version":9007199254740993,"activity_at":123}`, string(rows[id]))
	// Redis errors differ from an absent component.
	require.NoError(t, r.Set(ctx, f.Key(1, "card"), "wrong type", 0).Err())
	_, err = f.Get(ctx, []int64{1}, "card")
	require.Error(t, err)
}
