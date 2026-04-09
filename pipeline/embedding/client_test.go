package embedding

import (
	"context"
	"eigenflux_server/pkg/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIEmbeddingRequestIncludesConfiguredDimensions(t *testing.T) {
	t.Parallel()

	var gotReq EmbeddingRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3],"index":0}]}`))
	}))
	defer server.Close()

	client := NewClient("openai", "test-key", server.URL, "text-embedding-v4", 768)
	if _, err := client.GetEmbedding(context.Background(), "hello"); err != nil {
		t.Fatalf("GetEmbedding() error = %v", err)
	}

	if gotReq.Model != "text-embedding-v4" {
		t.Fatalf("request model = %q, want text-embedding-v4", gotReq.Model)
	}
	if gotReq.Dimensions != 768 {
		t.Fatalf("request dimensions = %d, want 768", gotReq.Dimensions)
	}
}

func TestOpenAIEmbeddingRequestOmitsDimensionsWhenNotConfigured(t *testing.T) {
	t.Parallel()

	var rawReq map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&rawReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3],"index":0}]}`))
	}))
	defer server.Close()

	client := NewClient("openai", "test-key", server.URL, "text-embedding-v4", 0)
	if _, err := client.GetEmbedding(context.Background(), "hello"); err != nil {
		t.Fatalf("GetEmbedding() error = %v", err)
	}

	if _, ok := rawReq["dimensions"]; ok {
		t.Fatalf("request unexpectedly included dimensions: %#v", rawReq["dimensions"])
	}
}
