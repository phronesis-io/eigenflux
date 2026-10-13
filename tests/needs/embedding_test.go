package needs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/pipeline/consumer"
	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/need"
	"eigenflux_server/rpc/sort/discovery/needembedding"
	"eigenflux_server/rpc/sort/discovery/queryprocessing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func isolatedVectorCache(t *testing.T, h *harness, endpoint string) *needembedding.Cache {
	t.Helper()
	addr := os.Getenv("DISCOVERY_TEST_REDIS")
	if addr == "" {
		t.Skip("DISCOVERY_TEST_REDIS required")
	}
	require.True(t, strings.HasPrefix(addr, "127.0.0.1:") || strings.HasPrefix(addr, "localhost:"), "isolated local Redis required")
	r := redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_PASSWORD")})
	require.NoError(t, r.Ping(context.Background()).Err())
	t.Cleanup(func() { r.Close() })
	schema := fmt.Sprintf("need_vectors_%d", h.owner)
	require.NoError(t, h.db.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() { h.db.Exec("DROP SCHEMA " + schema + " CASCADE") })
	require.NoError(t, h.db.Exec(fmt.Sprintf("CREATE VIEW %s.current_need_inputs AS SELECT * FROM public.current_need_inputs WHERE agent_id=%d", schema, h.owner)).Error)
	pg, err := pgx.ParseConfig(os.Getenv("PG_DSN"))
	require.NoError(t, err)
	pg.RuntimeParams["search_path"] = schema + ",public"
	conn := stdlib.OpenDB(*pg)
	t.Cleanup(func() { conn.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: conn}), &gorm.Config{})
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/000110_need_embedding_jobs.sql")
	require.NoError(t, err)
	require.NoError(t, db.Exec(strings.Split(string(migration), "-- +goose Down")[0]).Error)
	return needembedding.New(&config.Config{EmbeddingProvider: "openai", EmbeddingModel: "test-need", EmbeddingDimensions: 3, EmbeddingBaseURL: endpoint, DiscoveryEmbeddingRevision: fmt.Sprint(h.owner)}, db, r)
}

func TestNeedEmbeddingAsyncProductionAndReuse(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	var calls atomic.Int32
	var fail atomic.Bool
	var textSeen atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in struct {
			Input string `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		textSeen.Store(in.Input)
		if fail.Load() {
			http.Error(w, "unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"embedding":[1,0,0]}]}`)
	}))
	defer server.Close()
	cache := isolatedVectorCache(t, h, server.URL)
	embed := embedding.NewClient("openai", "", server.URL, "test-need", 3)
	worker := consumer.NeedEmbeddingWorker{Cache: cache, Embedder: embed}
	store := need.Store{DB: h.db, IDs: h.ids}
	input := need.Input{SchemaVersion: need.InputSchemaVersion, IntentID: h.intent, IntentVersion: 1, NeedType: "agent", Target: need.Target{Goal: "  ＳＱＬ   设计  ", Context: "Index review"}}
	raw, err := json.Marshal(input)
	require.NoError(t, err)
	saved, _, err := store.Create(ctx, h.owner, "embedding-async", raw, 1)
	require.NoError(t, err)
	text := queryprocessing.Process(queryprocessing.NeedText(input.Target.Goal, input.Target.Context), queryprocessing.Options{}).Normalized
	keys := []string{cache.Key(text)}
	t.Cleanup(func() { cache.Redis.Del(ctx, keys...) })
	// Save does not call the model. The background worker discovers it without an
	// online lookup or a best-effort queue publication.
	require.Zero(t, calls.Load())
	worked, err := worker.ProcessOne(ctx)
	require.NoError(t, err)
	require.True(t, worked)
	require.EqualValues(t, 1, calls.Load())
	require.Equal(t, text, textSeen.Load())
	for i := 0; i < 3; i++ {
		v, err := cache.Lookup(ctx, saved.NeedInputID, text, queryprocessing.Version)
		require.NoError(t, err)
		require.Equal(t, []float32{1, 0, 0}, v)
	}
	require.EqualValues(t, 1, calls.Load())
	// A different Need with identical processed text reuses the same vector.
	input.NeedType = "broadcast"
	raw, err = json.Marshal(input)
	require.NoError(t, err)
	_, _, err = store.Create(ctx, h.owner, "embedding-shared", raw, 2)
	require.NoError(t, err)
	worked, err = worker.ProcessOne(ctx)
	require.NoError(t, err)
	require.True(t, worked)
	require.EqualValues(t, 1, calls.Load())
	worked, err = worker.ProcessOne(ctx)
	require.NoError(t, err)
	require.False(t, worked)
	// Cache eviction causes async repair, not online model execution.
	require.NoError(t, cache.Redis.Del(ctx, cache.Key(text)).Err())
	_, err = cache.Lookup(ctx, saved.NeedInputID, text, queryprocessing.Version)
	require.ErrorIs(t, err, needembedding.ErrPending)
	require.EqualValues(t, 1, calls.Load())
	fail.Store(true)
	worked, err = worker.ProcessOne(ctx)
	require.Error(t, err)
	require.True(t, worked)
	require.EqualValues(t, 2, calls.Load())
	var before, after int64
	require.NoError(t, cache.DB.Raw("SELECT next_attempt_at FROM need_embedding_jobs WHERE need_input_id=?", saved.NeedInputID).Scan(&before).Error)
	_, err = cache.Lookup(ctx, saved.NeedInputID, text, queryprocessing.Version)
	require.ErrorIs(t, err, needembedding.ErrPending)
	require.NoError(t, cache.DB.Raw("SELECT next_attempt_at FROM need_embedding_jobs WHERE need_input_id=?", saved.NeedInputID).Scan(&after).Error)
	require.Equal(t, before, after, "online misses reset retry backoff")
	worked, err = worker.ProcessOne(ctx)
	require.NoError(t, err)
	require.False(t, worked)
	fail.Store(false)
	require.NoError(t, cache.DB.Exec("UPDATE need_embedding_jobs SET next_attempt_at=0 WHERE need_input_id=?", saved.NeedInputID).Error)
	worked, err = worker.ProcessOne(ctx)
	require.NoError(t, err)
	require.True(t, worked)
	// A model revision never reads the previous generation, and it discovers
	// existing inputs without recapturing or modifying them.
	upgraded := *cache
	upgraded.Profile.Revision += "-next"
	keys = append(keys, upgraded.Key(text))
	_, err = upgraded.Read(ctx, text)
	require.ErrorIs(t, err, needembedding.ErrPending)
	nextWorker := consumer.NeedEmbeddingWorker{Cache: &upgraded, Embedder: embed}
	worked, err = nextWorker.ProcessOne(ctx)
	require.NoError(t, err)
	require.True(t, worked)
	require.EqualValues(t, 4, calls.Load())
	_, err = cache.Read(ctx, text)
	require.NoError(t, err)
	_, err = upgraded.Read(ctx, text)
	require.NoError(t, err)
}

func TestNeedEmbeddingLeaseAndInactiveSources(t *testing.T) {
	h := setup(t)
	cache := isolatedVectorCache(t, h, "http://127.0.0.1:1")
	ctx := context.Background()
	store := need.Store{DB: h.db, IDs: h.ids}
	saved, _, err := store.Create(ctx, h.owner, "leased-embedding", reviewInput(h.intent, 1, "agent"), 1)
	require.NoError(t, err)
	now := time.Now().UnixMilli()
	first, err := cache.Claim(ctx, now)
	require.NoError(t, err)
	require.Equal(t, saved.NeedInputID, first.InputID)
	duplicate, err := cache.Claim(ctx, now)
	require.NoError(t, err)
	require.Zero(t, duplicate.InputID)
	second, err := cache.Claim(ctx, now+needembedding.LeaseDuration.Milliseconds()+1)
	require.NoError(t, err)
	require.Equal(t, first.InputID, second.InputID)
	require.NotEqual(t, first.LeaseToken, second.LeaseToken)
	require.NoError(t, cache.Finish(ctx, first, true, now))
	var ready bool
	require.NoError(t, cache.DB.Raw("SELECT ready FROM need_embedding_jobs WHERE need_input_id=?", saved.NeedInputID).Scan(&ready).Error)
	require.False(t, ready, "stale lease committed completion")
	require.NoError(t, h.db.Exec("UPDATE agent_intent_actions SET version=2 WHERE intent_id=?", h.intent).Error)
	job, err := cache.Claim(ctx, now+2*needembedding.LeaseDuration.Milliseconds())
	require.NoError(t, err)
	require.Zero(t, job.InputID)
}
