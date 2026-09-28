package featureindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/pkg/metrics"
	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type pipelineProbe struct {
	reads  atomic.Int64
	rounds atomic.Int64
	writes atomic.Int64
}

func (h *pipelineProbe) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}
func (h *pipelineProbe) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }
func (h *pipelineProbe) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		h.rounds.Add(1)
		for _, cmd := range cmds {
			if cmd.Name() == "hmget" {
				h.reads.Add(1)
			}
			if cmd.Name() == "evalsha" || cmd.Name() == "eval" {
				h.writes.Add(1)
			}
		}
		return next(ctx, cmds)
	}
}

func TestOnePipelinePrefetchAndRequestReuse(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	a := AgentIndex{Redis: r, IndexName: "test"}
	b := BroadcastIndex{Redis: r}
	c := CommissionIndex{Redis: r, IndexName: "test"}
	require.NoError(t, a.Write(ctx, AgentDocument{AgentID: 7, Version: 1, ProjectionVersion: 1, Active: true}))
	require.NoError(t, b.Write(ctx, BroadcastDocument{ItemID: 7, Version: 1, Active: true, ContentHash: "hash"}))
	require.NoError(t, c.Write(ctx, CommissionDocument{CommissionID: 7, CatalogueVersion: 1, StatisticsVersion: 1, Active: true}))
	probe := &pipelineProbe{}
	r.AddHook(probe)
	ctx = WithRequestCache(ctx)
	before := counterValue(metrics.FeatureReadItems.WithLabelValues("agent", Agent, "request_hit"))
	require.NoError(t, Prefetch(ctx, []ReadRequest{{a.Forward(), []int64{7, 7}, []string{"card"}}, {b.Forward(), []int64{7}, []string{"item"}}, {c.Forward(), []int64{7}, []string{"catalogue", "statistics"}}}))
	require.EqualValues(t, 1, probe.rounds.Load())
	require.EqualValues(t, 4, probe.reads.Load())
	for range 3 {
		ar, err := a.Read(ctx, []int64{7})
		require.NoError(t, err)
		require.True(t, ar[7].Active)
		br, err := b.Read(ctx, []int64{7})
		require.NoError(t, err)
		require.Equal(t, "hash", br[7].ContentHash)
		cr, err := c.Read(ctx, []int64{7})
		require.NoError(t, err)
		require.EqualValues(t, 1, cr[7].StatisticsVersion)
	}
	require.EqualValues(t, 4, probe.reads.Load(), "Need contexts must reuse features")
	require.Equal(t, before+3, counterValue(metrics.FeatureReadItems.WithLabelValues("agent", Agent, "request_hit")))
	_, err := a.Read(WithRequestCache(context.Background()), []int64{7})
	require.NoError(t, err)
	require.EqualValues(t, 5, probe.reads.Load(), "another request reads current features")
}

func TestBatchWritesKeepIndependentVersionsAndRecoverScriptEviction(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	store := AgentIndex{Redis: r, IndexName: "batch"}
	docs := make([]AgentDocument, 100)
	for i := range docs {
		docs[i] = AgentDocument{AgentID: int64(i + 1), Version: 10, ProjectionVersion: 10, Active: true}
	}
	require.NoError(t, r.Ping(ctx).Err())
	probe := &pipelineProbe{}
	r.AddHook(probe)
	require.NoError(t, store.WriteBatch(ctx, docs))
	require.LessOrEqual(t, probe.rounds.Load(), int64(2), "first batch can load a missing Lua script")
	require.NoError(t, r.ScriptFlush(ctx).Err())
	probe.rounds.Store(0)
	docs[0].ProjectionVersion = 9
	docs[0].Active = false
	docs[1].ProjectionVersion = 11
	docs[1].Active = false
	require.NoError(t, store.WriteBatch(ctx, docs))
	require.LessOrEqual(t, probe.rounds.Load(), int64(2))
	rows, err := store.Read(ctx, []int64{1, 2})
	require.NoError(t, err)
	require.True(t, rows[1].Active)
	require.False(t, rows[2].Active)
	docs[0].AgentID = 0
	require.Error(t, store.WriteBatch(ctx, docs))
}

func TestPhysicalRetentionDoesNotExtendForStaleWrites(t *testing.T) {
	server := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	f := (AgentIndex{Redis: r, IndexName: "retention"}).Forward()
	require.NoError(t, f.Put(ctx, 7, "card", 10, map[string]any{"agent_id": 7, "projection_version": 10}))
	server.FastForward(time.Hour)
	require.NoError(t, f.Put(ctx, 7, "card", 9, map[string]any{"agent_id": 7, "projection_version": 9}))
	require.Equal(t, 167*time.Hour, r.TTL(ctx, f.Key(7, "card")).Val(), "stale write must not extend retention")
	server.FastForward(168 * time.Hour)
	require.Zero(t, r.Exists(ctx, f.Key(7, "card")).Val())

}

type forbiddenText struct{}

func (forbiddenText) MarshalJSON() ([]byte, error) {
	return nil, errors.New("unregistered text must not be serialized")
}
func TestCodecSkipsUnregisteredSourceFieldsAndMasksOldPayloads(t *testing.T) {
	files := configFiles(t)
	registry, err := NewRegistry(files)
	require.NoError(t, err)
	view, err := registry.resolve("agent:test", "card")
	require.NoError(t, err)
	value := struct {
		ID      int64         `json:"agent_id"`
		Version int64         `json:"projection_version"`
		Text    forbiddenText `json:"search_text"`
	}{ID: 7, Version: 1}
	raw, err := view.project(7, 1, value)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "search_text")
	var d AgentDocument
	require.NoError(t, view.decode([]byte(`{"agent_id":7,"version":1,"projection_version":1,"search_text":"old text","activity_at":123}`), &d))
	require.Empty(t, d.SearchText)
	require.EqualValues(t, 123, d.ActivityAt)
	files["agent.card.yaml"].Data = []byte(strings.ReplaceAll(string(files["agent.card.yaml"].Data), "  - activity_at\n", ""))
	_, err = registry.Reload(files)
	require.NoError(t, err)
	view, err = registry.resolve("agent:test", "card")
	require.NoError(t, err)
	require.NoError(t, view.decode([]byte(`{"agent_id":7,"version":1,"projection_version":1,"activity_at":123}`), &d))
	require.Zero(t, d.ActivityAt)
}

func TestRequestCacheMissRepairGenerationAndReload(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	files := configFiles(t)
	registry, err := NewRegistry(files)
	require.NoError(t, err)
	f := Forward{Redis: r, Namespace: "agent:a", Registry: registry}
	ctx := WithRequestCache(context.Background())
	rows, err := f.Get(ctx, []int64{7}, "card")
	require.NoError(t, err)
	require.Empty(t, rows)
	value := map[string]any{"agent_id": 7, "projection_version": 1, "activity_at": 123}
	require.NoError(t, f.Put(ctx, 7, "card", 1, value))
	rows, err = f.Get(ctx, []int64{7}, "card")
	require.NoError(t, err)
	require.Contains(t, string(rows[7]), "123")
	other := f
	other.Namespace = "agent:b"
	rows, err = other.Get(ctx, []int64{7}, "card")
	require.NoError(t, err)
	require.Empty(t, rows)
	files["agent.card.yaml"].Data = []byte(strings.ReplaceAll(string(files["agent.card.yaml"].Data), "  - activity_at\n", ""))
	_, err = registry.Reload(files)
	require.NoError(t, err)
	rows, err = f.Get(ctx, []int64{7}, "card")
	require.NoError(t, err)
	require.NotContains(t, string(rows[7]), "activity_at")
	cache := requestCacheFrom(ctx)
	cache.put(requestKey{id: 100}, make([]byte, (4<<20)+1))
	require.LessOrEqual(t, cache.bytes, 4<<20)
}

func BenchmarkAgentFeatureDecode(b *testing.B) {
	view, _ := defaultRegistry.resolve("agent:bench", "card")
	raw := []byte(`{"agent_id":7,"version":1,"projection_version":1,"active":true,"activity_at":1790000000000,"updated_at":1790000000000,"retrieval_slots":{}}`)
	b.Run("typed", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var d AgentDocument
			if err := view.decode(raw, &d); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("map_json_typed", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			selected := map[string]json.RawMessage{}
			for _, name := range view.Fields {
				if v, ok := fields[name]; ok {
					selected[name] = v
				}
			}
			encoded, _ := json.Marshal(selected)
			var d AgentDocument
			_ = json.Unmarshal(encoded, &d)
		}
	})
}

func counterValue(c prometheus.Counter) float64 {
	m := &dto.Metric{}
	_ = c.Write(m)
	return m.GetCounter().GetValue()
}

func TestRetentionAuditRealRedis(t *testing.T) {
	addr := os.Getenv("DISCOVERY_TEST_REDIS")
	if addr == "" {
		t.Skip("isolated DISCOVERY_TEST_REDIS required")
	}
	r := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	f := Forward{Redis: r, Namespace: fmt.Sprintf("agent:audit-%d", time.Now().UnixNano())}
	legacy, key := f.Key(7, "card"), f.Key(8, "card")
	defer r.Del(ctx, legacy, key)
	require.NoError(t, r.HSet(ctx, legacy, "version", 11, "data", `{"agent_id":7,"projection_version":11}`).Err())
	require.NoError(t, r.HSet(ctx, key, "version", 11, "data", `{"agent_id":8,"projection_version":11}`).Err())
	require.NoError(t, r.Expire(ctx, key, time.Minute).Err())
	original := r.HGetAll(ctx, legacy).Val()
	var cursor uint64
	for {
		next, err := AuditPage(ctx, r, cursor, 100)
		require.NoError(t, err)
		cursor = next
		if next == 0 {
			break
		}
	}
	require.Equal(t, original, r.HGetAll(ctx, legacy).Val())
	require.InDelta(t, (168 * time.Hour).Seconds(), r.TTL(ctx, legacy).Val().Seconds(), 5)
	require.LessOrEqual(t, r.TTL(ctx, key).Val(), time.Minute)
	require.Positive(t, r.TTL(ctx, key).Val())
}

func TestStableBroadcastDoesNotForceFiveMinuteSourceRead(t *testing.T) {
	server := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { r.Close() })
	store := BroadcastIndex{Redis: r}
	ctx := context.Background()
	require.NoError(t, store.Write(ctx, BroadcastDocument{ItemID: 7, Version: 1, Active: true, ContentHash: "hash"}))
	server.FastForward(6 * time.Minute)
	rows, err := store.Read(ctx, []int64{7})
	require.NoError(t, err)
	require.True(t, rows[7].Active)
	server.FastForward(48 * time.Hour)
	_, err = store.Read(ctx, []int64{7})
	require.Error(t, err, "physically missing features need the source")
}

func TestRequestCacheDoesNotRememberRedisErrors(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	ctx := WithRequestCache(context.Background())
	f := Forward{Redis: r, Namespace: "agent:errors"}
	key := f.Key(7, "card")
	require.NoError(t, r.Set(ctx, key, "wrong-type", 0).Err())
	_, err := f.Get(ctx, []int64{7}, "card")
	require.Error(t, err)
	require.NoError(t, r.Del(ctx, key).Err())
	require.NoError(t, r.HSet(ctx, key, "data", `{"agent_id":7,"projection_version":1}`, "expires_at", 0).Err())
	rows, err := f.Get(ctx, []int64{7}, "card")
	require.NoError(t, err)
	require.Contains(t, rows, int64(7))
}
