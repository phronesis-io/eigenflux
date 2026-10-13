package featureindex

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"eigenflux_server/pkg/metrics"
	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCensusCountsPhysicalKeysAndPublishesZeroAfterExpiry(t *testing.T) {
	server := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	// Include old generations and tombstones: this measures storage, not candidates.
	keys := []string{
		"discovery:forward:agent:current:1:card",
		"discovery:forward:agent:retired:1:card",
		"discovery:forward:broadcast:v1:2:item",
		"discovery:forward:commission:current:3:catalogue",
		"discovery:forward:commission:current:3:statistics",
	}
	for _, k := range keys {
		require.NoError(t, r.HSet(ctx, k, "version", 1, "data", `{}`).Err())
	}
	require.NoError(t, r.Set(ctx, "unrelated", 1, 0).Err())
	require.NoError(t, r.Set(ctx, "discovery:forward:agent:current:bad:card", 1, 0).Err())
	require.NoError(t, r.Set(ctx, "discovery:forward:agent:current:4:unknown", 1, 0).Err())
	first, second := &Loaders{Redis: r}, &Loaders{Redis: r}
	// An older deployment snapshot may lack newly registered views. It must not
	// block the scanner from producing a complete current snapshot.
	require.NoError(t, r.HSet(ctx, auditPrefix+":snapshot", "_completed_at", 1, Agent, 99).Err())
	require.NoError(t, first.publishKeyCounts(ctx))
	for n := 0; n < 10; n++ {
		require.NoError(t, first.auditStep(ctx))
		if r.HExists(ctx, auditPrefix+":snapshot", "_completed_at").Val() {
			break
		}
		require.NoError(t, r.Del(ctx, auditPrefix+":next_due").Err())
	}
	snapshot := r.HGetAll(ctx, auditPrefix+":snapshot").Val()
	require.Equal(t, "2", snapshot[Agent])
	require.Equal(t, "1", snapshot[Broadcast])
	require.Equal(t, "1", snapshot[Commission])
	require.Equal(t, "1", snapshot[CommissionStatistics])
	require.NotEmpty(t, snapshot["_completed_at"])
	require.Positive(t, r.PTTL(ctx, keys[0]).Val())
	require.NoError(t, second.auditStep(ctx))
	require.Equal(t, snapshot, r.HGetAll(ctx, auditPrefix+":snapshot").Val(), "replicas respect the shared cycle pause")
	// Check the real Prometheus exposition, including labels and removed metrics.
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	require.Contains(t, rec.Body.String(), `feature_index_keys{type="agent",view="agent.card"} 2`)
	require.Contains(t, rec.Body.String(), `feature_index_keys{type="commission",view="commission.statistics"} 1`)
	for _, name := range []string{"feature_index_operation_seconds", "feature_index_batch_size", "feature_index_payload_bytes", "feature_index_audit_key_bytes", "feature_index_loader_cycle_seconds"} {
		require.NotContains(t, rec.Body.String(), name)
	}
	server.FastForward(8 * 24 * time.Hour)
	require.NoError(t, r.Del(ctx, auditPrefix+":next_due").Err())
	require.NoError(t, second.auditStep(ctx))
	snapshot = r.HGetAll(ctx, auditPrefix+":snapshot").Val()
	for _, view := range []string{Agent, Broadcast, Commission, CommissionStatistics} {
		require.Equal(t, "0", snapshot[view])
	}
}

func TestCensusCheckpointIsFencedAndHidesPartialCounts(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	keys := []string{auditPrefix + ":lock", auditPrefix + ":cursor", auditPrefix + ":counts", auditPrefix + ":snapshot", auditPrefix + ":next_due"}
	commit := func(token string, cursor, next int, counts map[string]int64) int {
		args := []any{token, cursor, next}
		for _, d := range Definitions() {
			args = append(args, d.Name, counts[d.Name])
		}
		n, err := finishAudit.Run(ctx, r, keys, args...).Int()
		require.NoError(t, err)
		return n
	}
	require.NoError(t, r.HSet(ctx, keys[3], Agent, 99).Err())
	require.NoError(t, r.Set(ctx, keys[0], "owner1", time.Minute).Err())
	require.Equal(t, 1, commit("owner1", 0, 17, map[string]int64{Agent: 2}))
	require.Equal(t, 0, commit("owner1", 0, 17, map[string]int64{Agent: 2}), "retried commit cannot count twice")
	require.Equal(t, "99", r.HGet(ctx, keys[3], Agent).Val(), "partial scans keep the previous snapshot")
	require.NoError(t, r.Set(ctx, keys[0], "owner2", time.Minute).Err())
	require.Equal(t, 0, commit("owner1", 17, 0, map[string]int64{Agent: 100}), "lost lease cannot publish")
	require.Equal(t, 1, commit("owner2", 17, 0, map[string]int64{Agent: 3, Commission: 1}))
	require.Equal(t, "5", r.HGet(ctx, keys[3], Agent).Val())
	require.Equal(t, "0", r.HGet(ctx, keys[3], Broadcast).Val())
	require.Equal(t, "1", r.HGet(ctx, keys[3], Commission).Val())
	require.Equal(t, "0", r.Get(ctx, keys[1]).Val())
	require.Zero(t, r.Exists(ctx, keys[2]).Val())
}

func TestMissRateCountsRedisLookupsWithoutRequestCacheReuse(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	ctx := WithRequestCache(context.Background())
	f := Forward{Redis: r, Namespace: "agent:miss-rate"}
	require.NoError(t, f.Put(ctx, 1, "card", 1, AgentDocument{AgentID: 1, Version: 1, ProjectionVersion: 1}))
	require.NoError(t, r.HSet(ctx, f.Key(3, "card"), "data", `{}`, "expires_at", time.Now().Add(-time.Minute).UnixMilli()).Err())
	outcomes := []string{"hit", "miss", "expired", "error", "request_hit"}
	before := map[string]float64{}
	for _, v := range outcomes {
		before[v] = counterValue(metrics.FeatureReadItems.WithLabelValues("agent", Agent, v))
	}
	for n := 0; n < 2; n++ {
		rows, err := f.Get(ctx, []int64{1, 2, 3, 1}, "card")
		require.NoError(t, err)
		require.Len(t, rows, 1)
	}
	delta := func(v string) float64 {
		return counterValue(metrics.FeatureReadItems.WithLabelValues("agent", Agent, v)) - before[v]
	}
	require.Equal(t, float64(1), delta("hit"))
	require.Equal(t, float64(1), delta("miss"))
	require.Equal(t, float64(1), delta("expired"))
	require.Equal(t, float64(3), delta("request_hit"))
	require.Equal(t, float64(0), delta("error"))
	require.InDelta(t, 2.0/3, (delta("miss")+delta("expired"))/(delta("hit")+delta("miss")+delta("expired")), 1e-9)
	require.NoError(t, r.Close())
	_, err := f.Get(WithRequestCache(context.Background()), []int64{1, 2, 3}, "card")
	require.Error(t, err)
	require.Equal(t, float64(3), delta("error"))
	require.Equal(t, float64(1), delta("miss"), "Redis errors must not inflate misses")
}
