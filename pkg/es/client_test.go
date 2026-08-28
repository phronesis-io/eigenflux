package es

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type esRoundTripFunc func(*http.Request) (*http.Response, error)

func (f esRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestInitClientWithTransportPerformsInfoOnly(t *testing.T) {
	previous := Client
	t.Cleanup(func() { Client = previous })
	requests := 0
	transport := esRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Method != http.MethodGet || req.URL.Path != "/" {
			t.Fatalf("unexpected Elasticsearch initialization request: %s %s", req.Method, req.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"X-Elastic-Product": []string{"Elasticsearch"}},
			Body:       io.NopCloser(strings.NewReader(`{"version":{"number":"8.11.0"},"tagline":"You Know, for Search"}`)),
		}, nil
	})

	if err := initClientWithTransport(transport); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("initialization requests=%d, want one Info request", requests)
	}
	if Client == nil {
		t.Fatal("Elasticsearch client was not initialized")
	}
}

func TestInitClientWithTransportRejectsFailedInfoWithoutReplacingClient(t *testing.T) {
	previous := Client
	transport := esRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"X-Elastic-Product": []string{"Elasticsearch"}},
			Body:       io.NopCloser(strings.NewReader(`{"secret":"must not escape"}`)),
		}, nil
	})
	if err := initClientWithTransport(transport); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe initialization error=%v", err)
	}
	if Client != previous {
		t.Fatal("failed initialization replaced the existing Elasticsearch client")
	}
}

func TestInitESWithTransportOwnsItemBootstrap(t *testing.T) {
	previousClient := Client
	previousDimensions := embeddingDims
	t.Cleanup(func() {
		Client = previousClient
		embeddingDims = previousDimensions
	})
	mutations := 0
	mappingReads := 0
	transport := esRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := `{}`
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/":
			body = `{"version":{"number":"8.11.0"},"tagline":"You Know, for Search"}`
		case req.Method == http.MethodPut:
			mutations++
			body = `{"acknowledged":true}`
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "_alias"):
			body = `{}`
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "_mapping"):
			mappingReads++
			body = `{"items-000001":{"mappings":{"properties":{"embedding":{"type":"dense_vector","dims":768}}}}}`
		default:
			t.Fatalf("unexpected InitES request: %s %s", req.Method, req.URL.Path)
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"X-Elastic-Product": []string{"Elasticsearch"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})

	if err := initESWithTransport(768, transport); err != nil {
		t.Fatal(err)
	}
	if mutations != 2 || mappingReads != 1 || EmbeddingDimensions() != 768 {
		t.Fatalf("mutations=%d mappingReads=%d dimensions=%d", mutations, mappingReads, EmbeddingDimensions())
	}
}
