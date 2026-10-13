package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestJetcacheWireTTLAndRawCompatibility(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { r.Close() })
	c := NewRedisCache(r)
	t.Cleanup(c.Close)
	require.NoError(t, c.Set(ctx, "json", "text", 250*time.Millisecond))
	stored, err := server.Get("json")
	require.NoError(t, err)
	require.Equal(t, `"text"`, stored)
	require.Equal(t, 250*time.Millisecond, server.TTL("json"))
	var str string
	require.NoError(t, c.Get(ctx, "json", &str))
	require.Equal(t, "text", str)
	require.NoError(t, c.SetBytes(ctx, "raw", []byte{0, 255, 42}, 0))
	raw, err := c.GetBytes(ctx, "raw")
	require.NoError(t, err)
	require.Equal(t, []byte{0, 255, 42}, raw)
	server.FastForward(time.Second)
	require.ErrorIs(t, c.Get(ctx, "json", &str), ErrCacheMiss)
	require.Equal(t, time.Duration(0), server.TTL("raw"))
	require.ErrorIs(t, c.SetBytes(ctx, "reserved", []byte("*"), time.Minute), ErrReservedValue)
	require.NoError(t, r.Set(ctx, "reserved", "*", time.Minute).Err())
	_, err = c.GetBytes(ctx, "reserved")
	require.ErrorIs(t, err, ErrReservedValue)
	server.SetError("unavailable")
	_, err = c.Exists(ctx, "json")
	require.Error(t, err)
}

func TestJetcacheTieringAndDelete(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { r.Close() })
	l := NewLRU(2, time.Minute)
	c, err := New(Config{Name: "tiered", Redis: r, Local: l, TTL: time.Minute})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.NoError(t, r.Set(ctx, "value", `{"n":7}`, time.Minute).Err())
	var out map[string]int
	require.NoError(t, c.Get(ctx, "value", &out))
	require.Equal(t, 7, out["n"])
	server.SetError("offline")
	require.NoError(t, c.Get(ctx, "value", &out)) // promoted L1 entry
	require.Error(t, c.Set(ctx, "value", map[string]int{"n": 8}, time.Minute))
	_, hit := l.Get("value")
	require.False(t, hit) // failed write cannot poison local
	server.SetError("")
	require.NoError(t, c.Delete(ctx, "value"))
	require.ErrorIs(t, c.Get(ctx, "value", &out), ErrCacheMiss)
}

func TestLocalLRUBoundsExpiryAndCopies(t *testing.T) {
	l := NewLRU(2, time.Second)
	l.Set("a", []byte("a"))
	l.Set("b", []byte("b"))
	raw, ok := l.Get("a")
	require.True(t, ok)
	raw[0] = 'x'
	l.Set("c", []byte("c"))
	_, ok = l.Get("b")
	require.False(t, ok)
	raw, ok = l.Get("a")
	require.True(t, ok)
	require.Equal(t, "a", string(raw))
	typed := NewLocal[string, int](1)
	now := time.Now()
	typed.PutUntil("a", 1, now.Add(time.Second))
	_, ok = typed.GetAt("a", now.Add(time.Second))
	require.False(t, ok)
	typed.PutUntil("b", 2, now.Add(time.Minute))
	_, ok = typed.Get("a")
	require.False(t, ok)
}

func TestJetcacheTinyLFUAdapter(t *testing.T) {
	l := NewTinyLFU(100, time.Minute)
	l.Set("key", []byte("value"))
	raw, ok := l.Get("key")
	require.True(t, ok)
	require.Equal(t, "value", string(raw))
	l.Del("key")
	_, ok = l.Get("key")
	require.False(t, ok)
}

type failSetHook struct{}

func (failSetHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (failSetHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (failSetHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "set" {
			return errors.New("write unavailable")
		}
		return next(ctx, cmd)
	}
}
func TestJetcacheLoadPropagatesWriteFailureAndNeverCachesErrors(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { r.Close() })
	r.AddHook(failSetHook{})
	c, err := New(Config{Name: "strict", Redis: r, TTL: time.Minute})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	var calls atomic.Int32
	loader := func(context.Context) (any, error) { calls.Add(1); return 42, nil }
	var out int
	for i := 0; i < 2; i++ {
		require.ErrorIs(t, c.Load(ctx, "k", &out, time.Second, loader), ErrUnavailable)
	}
	require.Equal(t, int32(2), calls.Load())
	require.False(t, server.Exists("k"))
	server.SetError("read unavailable")
	require.ErrorIs(t, c.Load(ctx, "k", &out, time.Second, loader), ErrUnavailable)
	require.Equal(t, int32(2), calls.Load())
}

func TestJetcacheSingleflightCancellationAndIsolation(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { r.Close() })
	c, err := New(Config{Name: "flights", Redis: r, TTL: time.Minute})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	entered := make(chan struct{})
	finish := make(chan struct{})
	var calls atomic.Int32
	loader := func(context.Context) (any, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-finish
		return []int{1}, nil
	}
	leader, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { var v []int; done <- c.Load(leader, "k", &v, time.Second, loader) }()
	<-entered
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	const n = 8
	var wg sync.WaitGroup
	results := make([][]int, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = c.Load(ctx, "k", &results[i], time.Second, loader) }(i)
	}
	close(finish)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), calls.Load())
	results[0][0] = 9
	require.Equal(t, 1, results[1][0])
}

func TestConnectionsShareAndClosePools(t *testing.T) {
	server := miniredis.RunT(t)
	var p Connections
	a := p.Client(server.Addr(), "")
	b := p.Client(server.Addr(), "")
	require.Same(t, a, b)
	require.NoError(t, a.Ping(context.Background()).Err())
	require.NoError(t, p.Close())
	require.Error(t, a.Ping(context.Background()).Err())
	require.NoError(t, p.Close())
}

// A UniversalClient may be a value wrapper, not only a client pointer.
type valueClient struct{ *redis.Client }

func TestValueClientAdapter(t *testing.T) {
	server := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { r.Close() })
	c, err := New(Config{Name: "wrapped", Redis: valueClient{r}, TTL: time.Second})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.NoError(t, c.Set(context.Background(), "k", 7, time.Second))
	var out int
	require.NoError(t, c.Get(context.Background(), "k", &out))
	require.Equal(t, 7, out)
}
