package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/need"
	"eigenflux_server/rpc/sort/discovery/needembedding"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func contextCacheFixture(t *testing.T) *cache.DiscoveryCache {
	t.Helper()
	server := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { r.Close() })
	return &cache.DiscoveryCache{Redis: r}
}
func TestCompiledContextReuseKeepsExecutionPrivateAndFresh(t *testing.T) {
	cached := contextCacheFixture(t)
	cc := Compiler{Cache: cached}
	ctx := context.Background()
	now := time.Now().UnixMilli()
	req := Request{Query: "  ＬＡＮＤＩＮＧ  design", SourceKinds: []Kind{Commission}, Filters: Filters{DeadlineMS: num(now + 10000), Lang: []string{"en"}}, SourceRevision: "2:3"}
	first, err := cc.Query(ctx, 1, 101, now, req, "agent_context")
	require.NoError(t, err)
	hash := first.SpecHash
	first.Filters.Lang[0] = "zh"
	first.QueryAnalysis.Normalized = "poison"
	first.Origins["lang"] = "poison"
	second, err := cc.Query(ctx, 1, 102, now+5000, req, "agent_context")
	require.NoError(t, err)
	require.Equal(t, hash, second.SpecHash)
	require.EqualValues(t, 102, second.ID)
	require.Equal(t, now+5000, second.CreatedAt)
	require.Equal(t, "landing design", second.QueryAnalysis.Normalized)
	require.Equal(t, []string{"en"}, second.Filters.Lang)
	require.Equal(t, "explicit", second.Origins["lang"])
	require.Empty(t, second.Vector)
	// Remaining delivery time is derived from this execution, not the cached clock.
	q, err := Query(second, Commission, "lexical", 10)
	require.NoError(t, err)
	raw, _ := json.Marshal(q)
	require.Contains(t, string(raw), `"promised_delivery_ms":{"lte":5000}`)
	_, err = cc.Query(ctx, 1, 103, now+10000, req, "agent_context")
	require.Error(t, err)
	// Changing a request filter must not reuse another request's constraints.
	req.Filters.Lang = []string{"zh"}
	req.Filters.DeadlineMS = nil
	third, err := cc.Query(ctx, 1, 104, now+5001, req, "agent_context")
	require.NoError(t, err)
	require.Equal(t, []string{"zh"}, third.Filters.Lang)
	require.NotEqual(t, hash, third.SpecHash)
	other, err := cc.Query(ctx, 2, 105, now+5001, req, "agent_context")
	require.NoError(t, err)
	require.Equal(t, []string{"2"}, other.Filters.ExcludeAuthors)
	keys, err := cached.Redis.Keys(ctx, "cache:discovery:*:compiled:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 3, "same query and source version should reuse the same compiled value")
	for _, key := range keys {
		value, err := cached.Redis.Get(ctx, key).Result()
		require.NoError(t, err)
		for _, field := range []string{"context_id", "created_at", "Warnings", "Vector", "embedding_pending"} {
			require.NotContains(t, value, field)
		}
	}
}
func TestCachedNeedRechecksEmbeddingAndDeadline(t *testing.T) {
	cc := Compiler{Cache: contextCacheFixture(t)}
	ctx := context.Background()
	now := time.Now().UnixMilli()
	saved := capturedFixture(44, Commission)
	editCaptured(t, &saved, func(in *need.Input) { in.Constraints.DeadlineMS = num(now + 1000) })
	calls := 0
	cc.NeedVectors = cacheLookupFunc(func(context.Context, int64, string, string) ([]float32, error) {
		calls++
		if calls == 1 {
			return nil, needembedding.ErrPending
		}
		return []float32{1, 0}, nil
	})
	first, err := cc.Need(ctx, 1, 100, now, saved)
	require.NoError(t, err)
	require.Contains(t, first.Warnings, "embedding_pending")
	first.CapturedNeed.Input[0] = 'x'
	warm, err := cc.Need(ctx, 1, 101, now+1, saved)
	require.NoError(t, err)
	require.Empty(t, warm.Warnings)
	require.NotEmpty(t, warm.Vector)
	require.JSONEq(t, string(saved.Input), string(warm.CapturedNeed.Input))
	_, err = cc.Need(ctx, 1, 102, now+1000, saved)
	require.Error(t, err)
	require.Equal(t, 2, calls, "expired cached Need must not even read a vector")
}

type countedNeeds struct {
	*memStore
	calls int
	read  func(int64) []need.Snapshot
}

func (s *countedNeeds) Active(_ context.Context, _ int64, _ []string, now int64) ([]need.Snapshot, error) {
	s.calls++
	return s.read(now), nil
}
func TestNeedSelectionCacheReplacesExpiredAndInvalidatedInputs(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UnixMilli()
	first := capturedFixture(1, Agent)
	editCaptured(t, &first, func(in *need.Input) { in.Constraints.DeadlineMS = num(now + 1000) })
	next := capturedFixture(2, Agent)
	source := &countedNeeds{memStore: &memStore{needs: map[int64]need.Snapshot{2: next}}, read: func(at int64) []need.Snapshot {
		if at < now+1000 {
			return []need.Snapshot{first}
		}
		return []need.Snapshot{next}
	}}
	cached := CachedNeeds{NeedReader: source, Cache: contextCacheFixture(t)}
	a, err := cached.Active(ctx, 1, []string{"agent"}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, a[0].InputID)
	_, err = cached.Active(ctx, 1, []string{"agent"}, now+500)
	require.NoError(t, err)
	require.Equal(t, 1, source.calls)
	a, err = cached.Active(ctx, 1, []string{"agent"}, now+1000)
	require.NoError(t, err)
	require.EqualValues(t, 2, a[0].InputID)
	source.read = func(int64) []need.Snapshot { return nil }
	cache.InvalidateDiscovery(ctx, cached.Cache.Redis, 1)
	a, err = cached.Active(ctx, 1, []string{"agent"}, now+1001)
	require.NoError(t, err)
	require.Empty(t, a)
	source.read = func(int64) []need.Snapshot { return []need.Snapshot{next} }
	cache.InvalidateDiscovery(ctx, cached.Cache.Redis, 1)
	a, err = cached.Active(ctx, 1, []string{"agent"}, now+1002)
	require.NoError(t, err)
	require.Len(t, a, 1)
	// Explicit ownership/lifecycle is never satisfied by automatic selection cache.
	delete(source.needs, 2)
	_, err = cached.Current(ctx, 1, 2)
	require.True(t, errors.Is(err, need.ErrNotFound))
}
func TestNegativeNeedCacheHasShortValidity(t *testing.T) {
	now := time.Now().UnixMilli()
	ctx := context.Background()
	source := &countedNeeds{read: func(int64) []need.Snapshot { return nil }}
	cached := CachedNeeds{NeedReader: source, Cache: contextCacheFixture(t)}
	_, err := cached.Active(ctx, 1, []string{"agent"}, now)
	require.NoError(t, err)
	source.read = func(int64) []need.Snapshot { return []need.Snapshot{capturedFixture(1, Agent)} }
	rows, err := cached.Active(ctx, 1, []string{"agent"}, now+100)
	require.NoError(t, err)
	require.Empty(t, rows)
	rows, err = cached.Active(ctx, 1, []string{"agent"}, now+cache.DiscoveryEmptyTTL.Milliseconds())
	require.NoError(t, err)
	require.Len(t, rows, 1)
}
