package consumer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/json"
	itemDal "eigenflux_server/rpc/item/dal"
	sortDal "eigenflux_server/rpc/sort/dal"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func seedConsumerItem(t *testing.T, status int16) int64 {
	t.Helper()
	gdb := consumerPersistenceDB(t)
	require.NoError(t, gdb.AutoMigrate(&itemDal.RawItem{}, &itemDal.ProcessedItem{}))
	require.NoError(t, gdb.Exec("ALTER TABLE processed_items ADD COLUMN retrieval_slots TEXT").Error)
	const id int64 = 713
	require.NoError(t, gdb.Create(&itemDal.RawItem{ItemID: id, AuthorAgentID: 991, RawContent: "Redis Streams keep unfinished work pending across a worker restart.", RawURL: "https://example.com/redis", CreatedAt: time.Now().UnixMilli()}).Error)
	require.NoError(t, gdb.Create(&itemDal.ProcessedItem{ItemID: id, Status: status, Summary: "Persisted summary", BroadcastType: "info", Domains: "infra,software", Keywords: "redis,streams", GroupID: 701, ExpectedResponse: "no_reply", Lang: "en", QualityScore: 0.9}).Error)
	return id
}

func TestItemConsumerCompletedCheckpointRetriesSearchWithoutLLMOrStatusRegression(t *testing.T) {
	id := seedConsumerItem(t, itemDal.StatusCompleted)
	var indexCalls atomic.Int32
	var indexed sortDal.Item
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3],"index":0}]}`))
			return
		}
		if indexCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"index temporarily unavailable"}`))
			return
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&indexed))
		_, _ = w.Write([]byte(`{"result":"updated"}`))
	}))
	defer server.Close()
	old := es.Client
	client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{server.URL}, DisableRetry: true})
	require.NoError(t, err)
	es.Client = client
	defer func() { es.Client = old }()
	// Nil LLM/safety clients deliberately prove completed checkpoints never call them.
	c := &ItemConsumer{embeddingClient: embedding.NewClient("openai", "local-test", server.URL, "text-embedding-v4", 3), qualityThreshold: 1}
	values := map[string]any{"item_id": strconv.FormatInt(id, 10)}
	require.Equal(t, HandleRetry, c.handle(context.Background(), "first", values))
	require.Equal(t, HandleSuccess, c.handle(context.Background(), "retry", values))
	require.Equal(t, int32(2), indexCalls.Load())
	require.Equal(t, id, indexed.ID)
	require.Equal(t, "Persisted summary", indexed.Summary)
	require.Equal(t, []string{"infra", "software"}, indexed.Domains)
	require.Equal(t, []string{"redis", "streams"}, indexed.Keywords)
	require.Equal(t, "no_reply", indexed.ExpectedResponse)
	processed, err := itemDal.GetProcessedItemByID(db.DB, id)
	require.NoError(t, err)
	require.Equal(t, itemDal.StatusCompleted, processed.Status)
	require.Equal(t, "Persisted summary", processed.Summary)
}

func TestPersistProcessedItemCancelledOwnerCannotOverwriteCompletedRow(t *testing.T) {
	id := seedConsumerItem(t, itemDal.StatusCompleted)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, persistProcessedItem(ctx, "expired", id, &llm.ExtractResult{Summary: "stale", BroadcastType: "alert"}, "", "", id, ""))
	processed, err := itemDal.GetProcessedItemByID(db.DB, id)
	require.NoError(t, err)
	require.Equal(t, itemDal.StatusCompleted, processed.Status)
	require.Equal(t, "Persisted summary", processed.Summary)
}

func TestNativeRedisItemPersistFailureStaysPendingThenCompletes(t *testing.T) {
	rdb, stream := nativeLeaseRedis(t)
	id := seedConsumerItem(t, itemDal.StatusProcessing)
	require.NoError(t, db.DB.Exec("CREATE TRIGGER reject_completion BEFORE UPDATE ON processed_items WHEN NEW.status=3 BEGIN SELECT RAISE(ABORT, 'temporary storage failure'); END").Error)
	failed, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	runner := launchLeaseRunner(ctx, leaseRunner(stream, func(ctx context.Context, msgID string, _ map[string]any) HandleResult {
		calls.Add(1)
		if !persistProcessedItem(ctx, msgID, id, &llm.ExtractResult{Summary: "Durable repaired summary", BroadcastType: "info"}, "infra", "no_reply", id, "") {
			close(failed)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return HandleRetry
		}
		return HandleSuccess
	}, 150*time.Millisecond))
	t.Cleanup(func() { cancel(); waitLeaseRunner(t, runner) })
	require.NoError(t, rdb.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{"item_id": strconv.FormatInt(id, 10)}}).Err())
	select {
	case <-failed:
	case <-time.After(2 * time.Second):
		t.Fatal("persistence failure not exercised")
	}
	require.Equal(t, int64(1), rdb.XPending(ctx, stream, "lease-test").Val().Count, "persistence helper must never acknowledge failed storage")
	require.Zero(t, rdb.XLen(ctx, stream+":dlq").Val())
	require.NoError(t, db.DB.Exec("DROP TRIGGER reject_completion").Error)
	close(release)
	require.Eventually(t, func() bool { return calls.Load() == 2 && rdb.XPending(ctx, stream, "lease-test").Val().Count == 0 }, 2*time.Second, 10*time.Millisecond)
	processed, err := itemDal.GetProcessedItemByID(db.DB, id)
	require.NoError(t, err)
	require.Equal(t, itemDal.StatusCompleted, processed.Status)
	require.Equal(t, "Durable repaired summary", processed.Summary)
	cancel()
	waitLeaseRunner(t, runner)
}
