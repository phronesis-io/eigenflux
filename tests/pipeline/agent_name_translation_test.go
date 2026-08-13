package pipeline_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"

	"github.com/stretchr/testify/require"
)

func agentNameModelServer(t *testing.T, output string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/responses", r.URL.Path)
		var request map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "translate-model", request["model"])
		input, _ := request["input"].(string)
		require.Contains(t, input, "<agent_name>信号狐</agent_name>")
		require.Contains(t, input, "untrusted data")
		require.Contains(t, input, "zero Han, Hiragana, Katakana, or Hangul characters")
		_, hasReasoning := request["reasoning"]
		require.False(t, hasReasoning)

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{
			"id":         "resp-agent-name",
			"object":     "response",
			"created_at": 1,
			"status":     "completed",
			"model":      "translate-model",
			"output": []map[string]interface{}{{
				"id": "msg-agent-name", "type": "message", "status": "completed", "role": "assistant",
				"content": []map[string]interface{}{{"type": "output_text", "text": output, "annotations": []interface{}{}}},
			}},
			"usage": map[string]interface{}{
				"input_tokens": 5, "output_tokens": 2, "total_tokens": 7,
				"output_tokens_details": map[string]interface{}{"reasoning_tokens": 0},
			},
		}))
	}))
}

func translateAgentName(t *testing.T, output string) (string, error) {
	t.Helper()
	server := agentNameModelServer(t, output)
	defer server.Close()
	client := llm.NewClient(&config.Config{
		LLMApiKey:    "test-key",
		LLMBaseURL:   server.URL,
		LLMModel:     "base-model",
		LLMMaxTokens: 128,
	}, nil).WithModel("translate-model").WithReasoningOff()
	return client.TranslateAgentNameToEnglish(context.Background(), "信号狐")
}

func TestTranslateAgentNameToEnglish(t *testing.T) {
	name, err := translateAgentName(t, `"Signal Fox"`)
	require.NoError(t, err)
	require.Equal(t, "Signal Fox", name)
}

func TestTranslateAgentNameRejectsNonEnglishOrMultilineOutput(t *testing.T) {
	for _, output := range []string{"信号狐", "Signal Fox\nExplanation"} {
		t.Run(strings.ReplaceAll(output, "\n", "_"), func(t *testing.T) {
			_, err := translateAgentName(t, output)
			require.Error(t, err)
		})
	}
}
