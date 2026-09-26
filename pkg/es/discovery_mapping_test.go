package es

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDiscoverySlotsUpgradeExistingIndices(t *testing.T) {
	prior := Client
	defer func() { Client = prior }()
	mapped := []string{}
	if err := initClientWithTransport(esRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"version":{"number":"8.11.0"},"tagline":"You Know, for Search"}`
		if strings.HasSuffix(r.URL.Path, "/_mapping") {
			raw, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(raw), `"type":"keyword"`) {
				t.Fatal("canonical IDs mapped as text")
			}
			mapped = append(mapped, r.URL.Path)
			body = `{"acknowledged":true}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := EnsureRetrievalSlots(context.Background(), "items-*", "commissions", "commissions"); err != nil {
		t.Fatal(err)
	}
	if len(mapped) != 2 {
		t.Fatal(mapped)
	}
}
