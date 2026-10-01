package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractKeywordsCorrectsContractViolationWithoutDroppingEntities(t *testing.T) {
	for _, bad := range []string{
		`{"keywords":["k1","k2","k3","k4","k5","k6","k7","k8","k9","k10","openclaw"],"country":"CN"}`,
		`{"keywords":["ai-agents","market-signals"],"country":"CN"}`,
		`{"keywords":["ai-agents","Market__Signals"],"country":"CN"}`,
	} {
		t.Run(bad, func(t *testing.T) {
			invalid := mockServer(t, bad)
			defer invalid.Close()
			valid := mockServer(t, `{"keywords":["ai-agents","openclaw"],"country":"CN,SG"}`)
			defer valid.Close()
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				var request struct{ Input string }
				if err := json.Unmarshal(body, &request); err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				if !strings.Contains(request.Input, "Original author bio: OpenClaw and AI agents") {
					t.Error("original bio missing")
				}
				r.Body = io.NopCloser(strings.NewReader(string(body)))
				if calls.Add(1) == 1 {
					invalid.Config.Handler.ServeHTTP(w, r)
					return
				}
				if !strings.Contains(request.Input, "-- OUTPUT VALIDATION") {
					t.Error("correction reason missing")
				}
				valid.Config.Handler.ServeHTTP(w, r)
			}))
			defer srv.Close()
			keywords, country, err := newTestClient(t, srv.URL).ExtractKeywords(context.Background(), "Original author bio: OpenClaw and AI agents")
			require.NoError(t, err)
			require.Equal(t, []string{"ai-agents", "openclaw"}, keywords)
			require.Equal(t, "CN,SG", country)
			require.Equal(t, int32(2), calls.Load())
		})
	}
}

func TestExtractKeywordsStopsAfterTwoInvalidResponses(t *testing.T) {
	invalid := mockServer(t, `{"keywords":["ai-agents","industry-insights"],"country":"CN"}`)
	defer invalid.Close()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); invalid.Config.Handler.ServeHTTP(w, r) }))
	defer srv.Close()
	keywords, country, err := newTestClient(t, srv.URL).ExtractKeywords(context.Background(), "AI agents")
	require.ErrorContains(t, err, "forbidden generic term")
	require.Nil(t, keywords)
	require.Empty(t, country)
	require.Equal(t, int32(2), calls.Load())
}

func TestExtractKeywordsValidSparseResultDoesNotRetryOrPad(t *testing.T) {
	valid := mockServer(t, `{"keywords":["openclaw"],"country":""}`)
	defer valid.Close()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); valid.Config.Handler.ServeHTTP(w, r) }))
	defer srv.Close()
	keywords, country, err := newTestClient(t, srv.URL).ExtractKeywords(context.Background(), "OpenClaw")
	require.NoError(t, err)
	require.Equal(t, []string{"openclaw"}, keywords)
	require.Empty(t, country)
	require.Equal(t, int32(1), calls.Load())
}
