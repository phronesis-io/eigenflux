package cache

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func discoveryCacheFixture(t *testing.T) (*DiscoveryCache, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { r.Close() })
	return &DiscoveryCache{Redis: r}, server
}
func TestDiscoveryCacheReuseIsolationAndExpiry(t *testing.T) {
	c, _ := discoveryCacheFixture(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	calls := 0
	load := func(context.Context) (any, int64, error) {
		calls++
		return map[string][]string{"terms": {"first"}}, now + 1000, nil
	}
	var a, b map[string][]string
	require.NoError(t, c.Load(ctx, 1, "owner", "v1", now, DiscoveryInputTTL, &a, load))
	a["terms"][0] = "mutated"
	// A separate process shares Redis, but never mutable request objects.
	other := &DiscoveryCache{Redis: c.Redis}
	require.NoError(t, other.Load(ctx, 1, "owner", "v1", now+1, DiscoveryInputTTL, &b, load))
	require.Equal(t, "first", b["terms"][0])
	require.Equal(t, 1, calls)
	require.NoError(t, c.Load(ctx, 2, "owner", "v1", now+1, DiscoveryInputTTL, &b, load))
	require.Equal(t, 2, calls)
	require.NoError(t, c.Load(ctx, 1, "owner", "v1", now+1000, DiscoveryInputTTL, &b, load))
	require.Greater(t, calls, 2, "absolute validity is checked even before Redis TTL expires")
	InvalidateDiscovery(ctx, c.Redis, 1)
	require.NoError(t, c.Load(ctx, 1, "owner", "v1", now+1, DiscoveryInputTTL, &b, load))
	require.Equal(t, "first", b["terms"][0])
}

func TestDiscoveryInvalidationFencesOldFill(t *testing.T) {
	c, _ := discoveryCacheFixture(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		var value string
		finished <- c.Load(ctx, 1, "owner", "v1", now, DiscoveryInputTTL, &value, func(context.Context) (any, int64, error) {
			close(entered)
			<-release
			return "old", 0, nil
		})
	}()
	<-entered
	InvalidateDiscovery(ctx, c.Redis, 1)
	var current string
	fresh := func(context.Context) (any, int64, error) { return "new", 0, nil }
	require.NoError(t, c.Load(ctx, 1, "owner", "v1", now, DiscoveryInputTTL, &current, fresh))
	require.Equal(t, "new", current)
	close(release)
	require.NoError(t, <-finished)
	require.NoError(t, c.Load(ctx, 1, "owner", "v1", now, DiscoveryInputTTL, &current, func(context.Context) (any, int64, error) {
		t.Fatal("new generation should stay warm")
		return nil, 0, nil
	}))
	require.Equal(t, "new", current)
}

func TestDiscoveryFlightsAndCanceledLeader(t *testing.T) {
	c, _ := discoveryCacheFixture(t)
	now := time.Now().UnixMilli()
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	load := func(ctx context.Context) (any, int64, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			return []string{"ready"}, 0, nil
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	leader := make(chan error, 1)
	go func() { var out []string; leader <- c.Load(ctx, 1, "owner", "v1", now, DiscoveryInputTTL, &out, load) }()
	<-entered
	cancel()
	require.ErrorIs(t, <-leader, context.Canceled)
	const n = 12
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			var out []string
			done <- c.Load(context.Background(), 1, "owner", "v1", now, DiscoveryInputTTL, &out, load)
		}()
	}
	close(release)
	for i := 0; i < n; i++ {
		require.NoError(t, <-done)
	}
	require.EqualValues(t, 1, calls.Load())
}

func TestDiscoveryDoesNotCacheSourceErrors(t *testing.T) {
	c, server := discoveryCacheFixture(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	var out string
	broken := errors.New("database unavailable")
	err := c.Load(ctx, 1, "owner", "v1", now, DiscoveryInputTTL, &out, func(context.Context) (any, int64, error) { return nil, 0, broken })
	require.ErrorIs(t, err, broken)
	load := func(context.Context) (any, int64, error) { return "recovered", 0, nil }
	require.NoError(t, c.Load(ctx, 1, "owner", "v1", now, DiscoveryInputTTL, &out, load))
	require.Equal(t, "recovered", out)
	// A cache outage remains a direct authoritative read, not an empty context.
	server.SetError("ERR cache unavailable")
	require.ErrorIs(t, c.Load(ctx, 1, "owner", "v1", now, DiscoveryInputTTL, &out, func(context.Context) (any, int64, error) { return nil, 0, broken }), broken)
	require.NoError(t, c.Load(ctx, 1, "owner", "v1", now, DiscoveryInputTTL, &out, load))
}

func TestDiscoveryGenerationExpiryDoesNotResurrectOldValues(t *testing.T) {
	c, server := discoveryCacheFixture(t)
	ctx := context.Background()
	var out string
	now := time.Now().UnixMilli()
	load := func(context.Context) (any, int64, error) { return "value", 0, nil }
	require.NoError(t, c.Load(ctx, 1, "compiled", "hash", now, 48*time.Hour, &out, load))
	old, err := c.Redis.Get(ctx, discoveryGenerationKey(1)).Result()
	require.NoError(t, err)
	server.FastForward(25 * time.Hour)
	require.NoError(t, c.Load(ctx, 1, "compiled", "hash", now+25*time.Hour.Milliseconds(), 48*time.Hour, &out, func(context.Context) (any, int64, error) { return "fresh", 0, nil }))
	require.Equal(t, "fresh", out)
	epoch, err := c.Redis.Get(ctx, discoveryGenerationKey(1)).Result()
	require.NoError(t, err)
	require.NotEqual(t, old, epoch)
	// Cache payloads contain values and expiry, never source text in their keys.
	for _, key := range server.Keys() {
		if key == discoveryGenerationKey(1) {
			continue
		}
		raw, err := c.Redis.Get(ctx, key).Bytes()
		require.NoError(t, err)
		require.True(t, json.Valid(raw))
	}
}
