package llm

import (
	"context"
	"eigenflux_server/pkg/config"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUGCMetadataDoesNotRequestSummary(t *testing.T) {
	calls := 0
	fixture := mockServer(t, `{"summary":"model ignored the contract","discard":false,"quality":0.7,"keywords":["coverage"],"homepage_eligible":true,"homepage_real_world_relevant":true,"homepage_evaluation_version":"homepage-v2"}`)
	defer fixture.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Contains(t, string(body), "Do not generate a summary")
		require.NotContains(t, string(body), "A concise 2-4 sentence summary")
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		fixture.Config.Handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := NewClient(&config.Config{LLMApiKey: "test", LLMBaseURL: srv.URL, LLMModel: "test-model", LLMMaxTokens: 4096}, testPromptRegistry(t))
	got, err := c.ProcessItemWithoutSummary(context.Background(), "canary coverage is incomplete", `{}`)
	require.NoError(t, err)
	require.Empty(t, got.Summary)
	require.Equal(t, 0.7, got.Quality)
	require.Equal(t, 1, calls)
}
func TestTranslationPromptPreservesUnknownAcronymsWithoutVerifier(t *testing.T) {
	calls := 0
	fixture := mockServer(t, "FTC 的批评指出覆盖不完整。")
	defer fixture.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Contains(t, string(body), "Never expand FTC")
		require.Contains(t, string(body), "Preserve negation")
		require.Contains(t, string(body), "SOURCE_JSON")
		require.Contains(t, string(body), "FTC 批评了该测试")
		require.Contains(t, string(body), "不猜译")
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		fixture.Config.Handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := NewClient(&config.Config{LLMApiKey: "test", LLMBaseURL: srv.URL, LLMModel: "test-model", LLMMaxTokens: 4096}, nil)
	got, err := c.TranslateToChinese(context.Background(), "FTC criticism says coverage is incomplete.")
	require.NoError(t, err)
	require.Equal(t, "FTC 的批评指出覆盖不完整。", got)
	require.Equal(t, 1, calls)
}
