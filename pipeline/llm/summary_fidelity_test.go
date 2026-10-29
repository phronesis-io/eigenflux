package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"eigenflux_server/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestSummaryFidelitySwitchReachesModelAndPreservesResponse(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			audit := mockServer(t, `{ "faithful":true,"unsupported":[],"correction":"" }`)
			defer audit.Close()
			valid := mockServer(t, `{"summary":"The statement announces a team led by A that reports to B.","discard":false,"quality":0.7,"keywords":["team"],"homepage_eligible":true,"homepage_real_world_relevant":true,"homepage_evaluation_version":"homepage-v2"}`)
			defer valid.Close()
			calls := 0
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
				isAudit := strings.Contains(request.Input, "You are auditing factual entailment")
				if !strings.Contains(request.Input, "A leads the team. It reports to B.") {
					t.Error("source body changed")
				}
				calls++
				r.Body = io.NopCloser(strings.NewReader(string(body)))
				if isAudit {
					audit.Config.Handler.ServeHTTP(w, r)
				} else {
					valid.Config.Handler.ServeHTTP(w, r)
				}
			}))
			defer srv.Close()
			c := NewClient(&config.Config{LLMApiKey: "test-key", LLMBaseURL: srv.URL, LLMModel: "test-model", LLMMaxTokens: 4096, LLMSummaryFidelity: enabled}, testPromptRegistry(t))
			result, err := c.ProcessPGCItem(context.Background(), "A leads the team. It reports to B.", "publisher: forwarding-agent")
			require.NoError(t, err)
			require.Equal(t, map[bool]int{false: 1, true: 2}[enabled], calls)
			// The same enabled shared client must not audit UGC or notes that
			// merely claim a PGC identity.
			before := calls
			ordinary, err := c.ProcessItem(context.Background(), "A leads the team. It reports to B.", "publisher: source@pgc.eigenflux.one")
			require.NoError(t, err)
			require.Equal(t, before+1, calls)
			require.Equal(t, result, ordinary)
			require.Equal(t, 0.7, result.Quality)
			require.Equal(t, []string{"team"}, result.Keywords)
			require.Equal(t, "The statement announces a team led by A that reports to B.", result.Summary)
		})
	}
}

func TestPGCPublisherSummaryReplacesServerSummaryWithoutAudit(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		if strings.Contains(string(body), "You are auditing factual entailment") {
			t.Error("publisher summary must not be re-audited")
		}
		calls++
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		mockServer(t, `{"summary":"Server rewrite.","discard":false,"quality":0.6,"keywords":["grid"],"homepage_eligible":true,"homepage_real_world_relevant":true,"homepage_evaluation_version":"homepage-v2"}`).Config.Handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	// The audit switch stays on: a publisher summary bypasses it anyway.
	c := NewClient(&config.Config{LLMApiKey: "test-key", LLMBaseURL: srv.URL, LLMModel: "test-model", LLMMaxTokens: 4096, LLMSummaryFidelity: true}, testPromptRegistry(t))
	result, err := c.ProcessPGCItemWithPublisherSummary(context.Background(), "Grid operator adds 2 GW.", `{"summary":"Grid operator adds 2 GW."}`, "Grid operator adds 2 GW.")
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, "Grid operator adds 2 GW.", result.Summary)
	require.Equal(t, 0.6, result.Quality)
	require.Equal(t, []string{"grid"}, result.Keywords)
}

func TestPGCPublisherSummaryKeepsDiscard(t *testing.T) {
	srv := mockServer(t, `{"discard":true,"discard_reason":"spam"}`)
	defer srv.Close()
	c := NewClient(&config.Config{LLMApiKey: "test-key", LLMBaseURL: srv.URL, LLMModel: "test-model", LLMMaxTokens: 4096}, testPromptRegistry(t))
	result, err := c.ProcessPGCItemWithPublisherSummary(context.Background(), "buy now", "", "One line.")
	require.NoError(t, err)
	require.True(t, result.Discard)
	require.Empty(t, result.Summary)
}
