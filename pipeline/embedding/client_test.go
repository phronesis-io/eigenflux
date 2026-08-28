package embedding

import (
	"context"
	"eigenflux_server/pkg/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

type embeddingRoundTripFunc func(*http.Request) (*http.Response, error)

func (f embeddingRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type countingResponseBody struct {
	reader io.Reader
	read   int64
}

func (b *countingResponseBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += int64(n)
	return n, err
}
func (*countingResponseBody) Close() error { return nil }

func TestEmbeddingResponsesAreBounded(t *testing.T) {
	for _, provider := range []string{"openai", "ollama"} {
		for _, status := range []int{http.StatusOK, http.StatusBadGateway} {
			t.Run(provider+http.StatusText(status), func(t *testing.T) {
				padding := strings.Repeat("x", int(maxEmbeddingResponseBytes+1))
				responseJSON := `{"padding":"` + padding + `","embedding":[0.1,0.2,0.3]}`
				if provider == "openai" {
					responseJSON = `{"padding":"` + padding + `","data":[{"embedding":[0.1,0.2,0.3],"index":0}]}`
				}
				body := &countingResponseBody{reader: strings.NewReader(responseJSON)}
				client := NewClient(provider, "test-key", "http://embedding.invalid", "model", 3)
				client.httpClient = &http.Client{Transport: embeddingRoundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: status, Body: body, Header: make(http.Header)}, nil
				})}
				_, err := client.GetEmbedding(context.Background(), "probe")
				if err == nil {
					t.Fatal("oversized embedding response accepted")
				}
				if body.read > maxEmbeddingResponseBytes+1 {
					t.Fatalf("response read %d bytes", body.read)
				}
				if len(err.Error()) > 160 || strings.Contains(err.Error(), padding[:80]) {
					t.Fatalf("unsafe error=%q", err)
				}
			})
		}
	}
}
