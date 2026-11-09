package consumer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	itemDal "eigenflux_server/rpc/item/dal"
	sortDal "eigenflux_server/rpc/sort/dal"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/stretchr/testify/require"
)

// Exercise the real worker through safety, extraction, persistence and projection
// recovery. Model/ES HTTP responses are deterministic, not a quality evaluation.
func TestPublisherOwnershipSurvivesWorkerAndProjectionRetry(t *testing.T) {
	for _, origin := range []string{"publisher", "none"} {
		t.Run(origin, func(t *testing.T) {
			id := seedConsumerItem(t, itemDal.StatusPending)
			setupItemConsumerRedis(t)
			text := "  FTC v0.4 is NOT proven safe  "
			if origin == "none" {
				text = ""
			}
			require.NoError(t, db.DB.Table("processed_items").Where("item_id = ?", id).Updates(map[string]any{"summary": text, "summary_origin": origin}).Error)
			var calls, indexes atomic.Int32
			var indexed sortDal.Item
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Elastic-Product", "Elasticsearch")
				switch {
				case strings.HasSuffix(r.URL.Path, "/embeddings"):
					_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3],"index":0}]}`))
				case strings.HasSuffix(r.URL.Path, "/responses"):
					calls.Add(1)
					output := `{"homepage_eligible":true,"homepage_real_world_relevant":true,"homepage_evaluation_version":"homepage-v2","safe":true,"summary":"invented model summary","discard":false,"quality":0.9,"broadcast_type":"info","domains":["infra"],"keywords":["redis"],"lang":"en","suggestion":"Read the original."}`
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp-test", "object": "response", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": output}}}}})
				case strings.HasSuffix(r.URL.Path, "/_search"):
					_, _ = w.Write([]byte(`{"hits":{"hits":[]}}`))
				default:
					require.NoError(t, json.NewDecoder(r.Body).Decode(&indexed))
					if indexes.Add(1) == 2 {
						w.WriteHeader(503)
						_, _ = w.Write([]byte(`{"error":"retry projection"}`))
						return
					}
					_, _ = w.Write([]byte(`{"result":"updated"}`))
				}
			}))
			defer server.Close()
			old := es.Client
			client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{server.URL}, DisableRetry: true})
			require.NoError(t, err)
			es.Client = client
			defer func() { es.Client = old }()
			prompts, err := llm.LoadPrompts("../../static/templates/prompts")
			require.NoError(t, err)
			lc := llm.NewClient(&config.Config{LLMApiKey: "fixture", LLMBaseURL: server.URL, LLMModel: "fixture", LLMMaxTokens: 4096}, prompts)
			c := &ItemConsumer{llmClient: lc, safetyClient: lc, embeddingClient: embedding.NewClient("openai", "fixture", server.URL, "fixture", 3)}
			values := map[string]any{"item_id": strconv.FormatInt(id, 10)}
			require.Equal(t, HandleRetry, c.handle(context.Background(), "first", values))
			before := calls.Load()
			require.Equal(t, int32(3), before, "safety, metadata and suggestion only")
			require.Equal(t, HandleSuccess, c.handle(context.Background(), "retry", values))
			require.Equal(t, before, calls.Load(), "completed retry must not regenerate")
			row, err := itemDal.GetProcessedItemByID(db.DB, id)
			require.NoError(t, err)
			require.Equal(t, itemDal.StatusCompleted, row.Status)
			require.Equal(t, text, row.Summary)
			require.Equal(t, origin, row.SummaryOrigin)
			require.Equal(t, text, indexed.Summary)
		})
	}
}
