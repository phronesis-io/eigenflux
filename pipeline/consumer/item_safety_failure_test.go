package consumer

import (
	"context"
	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	itemDal "eigenflux_server/rpc/item/dal"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestNativeRedisSafetyEvaluationFailureRemainsUndelivered(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		storageFails, unsafe bool
		want                 HandleResult
		status               int16
	}{
		{"provider refusal", false, false, HandleFailure, itemDal.StatusFailed},
		{"failure persistence unavailable", true, false, HandleRetry, itemDal.StatusProcessing},
		{"actual unsafe verdict", false, true, HandleSuccess, itemDal.StatusDiscarded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nativeLeaseRedis(t)
			id := seedConsumerItem(t, itemDal.StatusPending)
			if tc.storageFails {
				require.NoError(t, db.DB.Exec("CREATE TRIGGER reject_failure BEFORE UPDATE ON processed_items WHEN NEW.status=2 BEGIN SELECT RAISE(ABORT, 'storage unavailable'); END").Error)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Elastic-Product", "Elasticsearch")
				switch {
				case strings.HasSuffix(r.URL.Path, "/embeddings"):
					_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3],"index":0}]}`))
				case strings.HasSuffix(r.URL.Path, "/responses"):
					if tc.unsafe {
						_, _ = w.Write([]byte(`{"id":"r","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"safe\":false,\"flag\":\"political_sensitive\",\"reason\":\"test verdict\"}"}]}]}`))
						return
					}
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"code":"DataInspectionFailed","message":"provider refused evaluation","type":"invalid_request_error"}}`))
				default:
					_, _ = w.Write([]byte(`{"hits":{"hits":[]}}`))
				}
			}))
			defer server.Close()
			old := es.Client
			client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{server.URL}, DisableRetry: true})
			require.NoError(t, err)
			es.Client = client
			defer func() { es.Client = old }()
			prompts, err := llm.LoadDefaultPrompts()
			require.NoError(t, err)
			c := &ItemConsumer{safetyClient: llm.NewSafetyClient(&config.Config{SafetyLLMBaseURL: server.URL, SafetyLLMApiKey: "test", SafetyLLMModel: "test", LLMMaxTokens: 256}, prompts), embeddingClient: embedding.NewClient("openai", "test", server.URL, "test", 3)}
			// No extraction client: any fall-through past safety fails the test.
			require.Equal(t, tc.want, c.handle(context.Background(), "test", map[string]any{"item_id": strconv.FormatInt(id, 10)}))
			row, err := itemDal.GetProcessedItemByID(db.DB, id)
			require.NoError(t, err)
			require.Equal(t, tc.status, row.Status)
			if !tc.unsafe {
				require.Empty(t, row.DistributionSkipReason)
			}
		})
	}
}
