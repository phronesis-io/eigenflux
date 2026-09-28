package featureindex

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestLoaderRetriesSamePageAndFencesCheckpoint(t *testing.T) {
	server := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	m := &Loaders{Redis: r}
	calls := []int64{}
	fail := true
	steal := false
	index := &testMaterializer{view: Broadcast, generation: "v1"}
	l := Loader{Index: index, Interval: time.Minute, Timeout: time.Second, BatchSize: 100}
	key := "discovery:feature:loader:" + Broadcast + ":v1"
	index.loadPage = func(ctx context.Context, cursor int64, limit int) (int64, error) {
		require.Equal(t, 100, limit)
		calls = append(calls, cursor)
		if fail {
			return 0, errors.New("source unavailable")
		}
		if steal {
			require.NoError(t, r.Set(ctx, key+":lock", "new-owner", time.Minute).Err())
		}
		return cursor + 100, nil
	}
	require.NoError(t, m.Register(l))
	require.Error(t, m.Register(l))
	require.Error(t, m.Step(ctx, l))
	require.False(t, server.Exists(key+":cursor"))
	fail = false
	require.NoError(t, m.Step(ctx, l))
	require.Equal(t, "100", r.Get(ctx, key+":cursor").Val())
	steal = true
	require.Error(t, m.Step(ctx, l))
	require.Equal(t, "100", r.Get(ctx, key+":cursor").Val())
	require.Equal(t, []int64{0, 0, 100}, calls)
	require.Equal(t, "new-owner", r.Get(ctx, key+":lock").Val())
}
func TestLoaderCancellationDoesNotAdvance(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	l := Loader{Index: &testMaterializer{view: Agent, generation: "test", loadPage: func(c context.Context, _ int64, _ int) (int64, error) { cancel(); return 10, c.Err() }}, Interval: time.Hour, Timeout: time.Second, BatchSize: 1}
	m := &Loaders{Redis: r}
	require.NoError(t, m.Register(l))
	require.Error(t, m.Step(ctx, l))
	require.Equal(t, redis.Nil, r.Get(context.Background(), "discovery:feature:loader:agent.card:test:cursor").Err())
}

type testMaterializer struct {
	view, generation string
	loadPage         func(context.Context, int64, int) (int64, error)
}

func (s *testMaterializer) View() string       { return s.view }
func (s *testMaterializer) Generation() string { return s.generation }
func (s *testMaterializer) LoadPage(ctx context.Context, cursor int64, limit int) (int64, error) {
	return s.loadPage(ctx, cursor, limit)
}

func TestLoaderCyclePauseIsSharedAcrossInstances(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	calls := 0
	index := &testMaterializer{view: Agent, generation: "paced", loadPage: func(context.Context, int64, int) (int64, error) { calls++; return 0, nil }}
	l := Loader{Index: index, Interval: time.Second, Timeout: time.Second, BatchSize: 100, CyclePause: time.Minute, DynamicConfig: true}
	first, second := &Loaders{Redis: r}, &Loaders{Redis: r}
	ctx := context.Background()
	require.NoError(t, first.Step(ctx, l))
	require.NoError(t, second.Step(ctx, l))
	require.Equal(t, 1, calls)
	prefix := "discovery:feature:loader:agent.card:paced"
	require.NoError(t, r.Del(ctx, prefix+":next_due").Err())
	require.NoError(t, second.Step(ctx, l))
	require.Equal(t, 2, calls)
}
