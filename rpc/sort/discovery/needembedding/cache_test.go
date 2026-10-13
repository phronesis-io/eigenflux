package needembedding

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type embedFunc func(context.Context, string) ([]float32, error)

func (f embedFunc) GetEmbedding(ctx context.Context, s string) ([]float32, error) { return f(ctx, s) }
func testCache(t *testing.T) *Cache {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	return &Cache{Redis: r, Profile: Profile{Model: "test", Provider: "openai", Dimensions: 2, Processor: "query-v1"}}
}
func TestCacheIdentityAndValidation(t *testing.T) {
	c := testCache(t)
	key := c.Key("private need wording")
	require.NotContains(t, key, "private")
	require.NotEqual(t, key, c.Key("different wording"))
	for _, change := range []func(*Profile){func(p *Profile) { p.Model = "other" }, func(p *Profile) { p.Revision = "r2" }, func(p *Profile) { p.Processor = "query-v2" }, func(p *Profile) { p.Dimensions = 3 }, func(p *Profile) { p.Provider = "ollama" }, func(p *Profile) { p.Endpoint = "other" }} {
		other := *c
		change(&other.Profile)
		require.NotEqual(t, key, other.Key("private need wording"))
		require.NotEqual(t, c.Generation(), other.Generation())
	}
	ctx := context.Background()
	for _, v := range [][]float32{nil, {1}, {0, 0}, {float32(math.NaN()), 1}, {float32(math.Inf(1)), 1}} {
		_, err := c.Produce(ctx, "bad", embedFunc(func(context.Context, string) ([]float32, error) { return v, nil }))
		require.ErrorIs(t, err, ErrInvalid)
		require.Zero(t, c.Redis.Exists(ctx, c.Key("bad")).Val())
	}
	var calls int
	embed := embedFunc(func(context.Context, string) ([]float32, error) { calls++; return []float32{1, 0}, nil })
	for i := 0; i < 2; i++ {
		v, err := c.Produce(ctx, "same", embed)
		require.NoError(t, err)
		require.Equal(t, []float32{1, 0}, v)
	}
	require.Equal(t, 1, calls)
	require.Greater(t, c.Redis.TTL(ctx, c.Key("same")).Val(), 29*24*time.Hour)
	require.NoError(t, c.Redis.Set(ctx, c.Key("same"), strings.Repeat("x", 100), 0).Err())
	_, err := c.Read(ctx, "same")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = c.Produce(ctx, "same", embed)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}
func TestConcurrentProductionAndProviderFailure(t *testing.T) {
	c := testCache(t)
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	var calls atomic.Int32
	embed := embedFunc(func(context.Context, string) ([]float32, error) {
		calls.Add(1)
		close(started)
		<-release
		return []float32{1, 0}, nil
	})
	go func() { _, err := c.Produce(ctx, "same", embed); done <- err }()
	<-started
	_, err := c.Produce(ctx, "same", embed)
	require.ErrorIs(t, err, ErrBusy)
	close(release)
	require.NoError(t, <-done)
	require.EqualValues(t, 1, calls.Load())
	_, err = c.Produce(ctx, "failed", embedFunc(func(context.Context, string) ([]float32, error) { return nil, errors.New("unavailable") }))
	require.Error(t, err)
	require.Zero(t, c.Redis.Exists(ctx, c.Key("failed")).Val())
	require.Zero(t, c.Redis.Exists(ctx, c.Key("failed")+":lock").Val())
}
