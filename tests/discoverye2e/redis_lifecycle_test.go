package discoverye2e

import (
	"context"
	"testing"

	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/mq"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryFixtureRedisLifecycle(t *testing.T) {
	server := miniredis.RunT(t)
	var pools cache.Connections
	t.Cleanup(func() { require.NoError(t, pools.Close()) })
	previous := mq.RDB
	borrowed := pools.Client(server.Addr(), "")
	mq.RDB = borrowed
	t.Cleanup(func() { mq.RDB = previous })
	var first *redis.Client
	t.Run("first fixture", func(t *testing.T) {
		initFixtureRedis(t, server.Addr(), "")
		first = mq.RDB
		require.NotSame(t, borrowed, first)
		require.NoError(t, mq.RDB.Set(context.Background(), "fixture-value", "retained", 0).Err())
	})
	require.Same(t, borrowed, mq.RDB)
	require.NoError(t, borrowed.Ping(context.Background()).Err(), "fixture cleanup must not close a borrowed shared client")
	t.Run("second fixture", func(t *testing.T) {
		initFixtureRedis(t, server.Addr(), "")
		require.NotSame(t, first, mq.RDB)
		value, err := mq.RDB.Get(context.Background(), "fixture-value").Result()
		require.NoError(t, err)
		require.Equal(t, "retained", value, "the next fixture gets a live client without resetting store data")
	})
	require.Same(t, borrowed, mq.RDB)
	require.NoError(t, pools.Client(server.Addr(), "").Ping(context.Background()).Err())
}
