package consumer

import (
	"context"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/mq"
	itemDal "eigenflux_server/rpc/item/dal"
	"encoding/json"
	"github.com/alicebob/miniredis/v2"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSlopCheckpointBlocksAtEveryThresholdAndRetriesCleanup(t *testing.T) {
	for _, threshold := range []float64{0, 0.4} {
		t.Run(strconv.FormatFloat(threshold, 'f', 1, 64), func(t *testing.T) {
			id := seedConsumerItem(t, itemDal.StatusProcessing)
			require.NoError(t, db.DB.AutoMigrate(&itemDal.ItemSlopEvaluation{}))
			raw, err := itemDal.GetRawItemByID(db.DB, id)
			require.NoError(t, err)
			one := 1
			score, quality := 0.9, 0.8
			zero := 0.0
			yes := true
			r := llm.UGCExtractResult{ExtractResult: llm.ExtractResult{Quality: quality, HomepageEligible: &yes}, IsSlop: &one, SlopReason: "S2: internal procedure.", SlopScore: &score}
			body, err := json.Marshal(r)
			require.NoError(t, err)
			row := &itemDal.ItemSlopEvaluation{ItemID: id, PromptVersion: llm.SlopVersion, ContentHash: llm.SlopHash(raw.RawContent + "\x00" + raw.RawNotes), Response: string(body), IsSlop: &one, SlopReason: r.SlopReason, SlopScore: &score, ModelQuality: &quality, EffectiveQuality: &zero, Blocked: true}
			_, err = itemDal.SaveSlopEvaluation(db.DB, row)
			require.NoError(t, err)
			// A repeat with a conflicting model vote must retain the committed decision.
			other := *row
			other.Blocked = false
			nonSlop := 0
			other.IsSlop = &nonSlop
			saved, err := itemDal.SaveSlopEvaluation(db.DB, &other)
			require.NoError(t, err)
			require.True(t, saved.Blocked)
			c := &ItemConsumer{ugcSlopEnabled: true, qualityThreshold: threshold} // nil clients forbid model calls
			// Simulate a duplicate delivery that read Processing before the
			// first block committed and then wrote its stale processing state.
			require.NoError(t, itemDal.UpdateProcessedItemStatus(db.DB, id, itemDal.StatusProcessing))
			result, err := c.processUGC(context.Background(), raw)
			require.NoError(t, err)
			require.Zero(t, result.Quality)
			require.True(t, result.HomepageEvaluationIncomplete, "raw checkpoint lacks real-world relevance")
			llm.NormalizeHomepageEvaluation(result)
			require.Empty(t, result.HomepageEvaluationVersion, "checkpoint replay must preserve the backfill marker")
			var deletes atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodPost, r.Method)
				require.Contains(t, r.URL.Path, "_delete_by_query")
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Elastic-Product", "Elasticsearch")
				if deletes.Add(1) == 1 {
					w.WriteHeader(503)
					_, _ = w.Write([]byte(`{"error":"unavailable"}`))
					return
				}
				_, _ = w.Write([]byte(`{"deleted":1,"failures":[]}`))
			}))
			defer s.Close()
			old := es.Client
			defer func() { es.Client = old }()
			es.Client, err = elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{s.URL}, DisableRetry: true})
			require.NoError(t, err)
			values := map[string]any{"item_id": strconv.FormatInt(id, 10)}
			require.Equal(t, HandleRetry, c.handle(context.Background(), "first", values))
			require.Equal(t, HandleSuccess, c.handle(context.Background(), "retry", values))
			p, err := itemDal.GetProcessedItemByID(db.DB, id)
			require.NoError(t, err)
			require.Equal(t, itemDal.StatusDiscarded, p.Status)
			require.Equal(t, int32(2), deletes.Load())
		})
	}
}

func TestSlopNonSlopZeroAndDiscardRemainDistinct(t *testing.T) {
	for _, discard := range []bool{false, true} {
		t.Run(strconv.FormatBool(discard), func(t *testing.T) {
			id := seedConsumerItem(t, itemDal.StatusProcessing)
			require.NoError(t, db.DB.AutoMigrate(&itemDal.ItemSlopEvaluation{}))
			zero := 0
			score := 0.1
			q := 0.0
			row := &itemDal.ItemSlopEvaluation{ItemID: id, PromptVersion: llm.SlopVersion, ContentHash: "hash", Discard: discard}
			if !discard {
				row.IsSlop = &zero
				row.SlopScore = &score
				row.ModelQuality = &q
				row.EffectiveQuality = &q
				row.SlopReason = "E1: concrete task."
			}
			_, err := itemDal.SaveSlopEvaluation(db.DB, row)
			require.NoError(t, err)
			blocked, err := itemDal.IsSlopBlocked(db.DB, id)
			require.NoError(t, err)
			require.False(t, blocked)
			p, err := itemDal.GetProcessedItemByID(db.DB, id)
			require.NoError(t, err)
			require.Equal(t, itemDal.StatusProcessing, p.Status)
		})
	}
}

// A full handler pass proves there is no draft indexing, action suggestion or
// completed projection before/after a positive Slop verdict.
func TestSlopHandleStopsBeforeAnyPublicProjection(t *testing.T) {
	id := seedConsumerItem(t, itemDal.StatusPending)
	require.NoError(t, db.DB.AutoMigrate(&itemDal.ItemSlopEvaluation{}))
	require.NoError(t, db.DB.Exec("CREATE TABLE agents(agent_id BIGINT PRIMARY KEY,email TEXT); INSERT INTO agents VALUES (991,'ugc@example.test')").Error)
	rdb := miniredis.RunT(t)
	oldRedis := mq.RDB
	mq.RDB = redis.NewClient(&redis.Options{Addr: rdb.Addr()})
	defer func() { mq.RDB.Close(); mq.RDB = oldRedis }()
	require.NoError(t, mq.RDB.Set(context.Background(), blacklistCacheKey, "[]", 0).Err())
	var modelCalls, indexCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		switch {
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3],"index":0}]}`))
		case strings.HasSuffix(r.URL.Path, "/responses"):
			n := modelCalls.Add(1)
			text := `{"safe":true,"flag":"","reason":"safe"}`
			if n == 2 {
				text = `{"discard":false,"is_slop":1,"slop_reason":"S2: local retry mechanics without an external task.","slop_score":0.9,"quality":0.8,"homepage_eligible":true,"homepage_real_world_relevant":true,"homepage_evaluation_version":"homepage-v2"}`
			}
			require.LessOrEqual(t, n, int32(2), "must never call suggest_action")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"id": "resp-test", "object": "response", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}}}))
		case strings.Contains(r.URL.Path, "_search"):
			_, _ = w.Write([]byte(`{"hits":{"hits":[]}}`))
		case strings.Contains(r.URL.Path, "_delete_by_query"):
			_, _ = w.Write([]byte(`{"deleted":0,"failures":[]}`))
		default:
			indexCalls.Add(1)
			t.Errorf("unexpected projection request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	oldES := es.Client
	defer func() { es.Client = oldES }()
	var err error
	es.Client, err = elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{server.URL}, DisableRetry: true})
	require.NoError(t, err)
	reg, err := llm.LoadDefaultPrompts()
	require.NoError(t, err)
	c := NewItemConsumer(&config.Config{UGCSlopEnabled: true, UGCSlopRolloutBPS: 10000, LLMBaseURL: server.URL, LLMModel: "test", LLMMaxTokens: 4096, SafetyLLMBaseURL: server.URL, EmbeddingProvider: "openai", EmbeddingBaseURL: server.URL, EmbeddingModel: "test", EmbeddingDimensions: 3, PGCEmailSuffixes: []string{"@pgc.example.test"}}, reg)
	values := map[string]any{"item_id": strconv.FormatInt(id, 10)}
	require.Equal(t, HandleSuccess, c.handle(context.Background(), "first", values))
	require.Equal(t, HandleSuccess, c.handle(context.Background(), "duplicate", values))
	require.Equal(t, int32(2), modelCalls.Load())
	require.Zero(t, indexCalls.Load())
	p, err := itemDal.GetProcessedItemByID(db.DB, id)
	require.NoError(t, err)
	require.Equal(t, itemDal.StatusDiscarded, p.Status)
	row, err := itemDal.GetSlopEvaluation(db.DB, id)
	require.NoError(t, err)
	require.True(t, row.Blocked)
	require.Equal(t, 0.8, *row.ModelQuality)
	require.Zero(t, *row.EffectiveQuality)
}
