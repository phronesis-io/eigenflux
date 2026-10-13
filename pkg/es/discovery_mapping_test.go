package es

import (
	"context"
	"encoding/json"
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
	if err := EnsureBroadcastRetrievalFields(context.Background(), "items-*"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureRetrievalSlots(context.Background(), "commissions", "commissions"); err != nil {
		t.Fatal(err)
	}
	if len(mapped) != 2 {
		t.Fatal(mapped)
	}
}

func TestBroadcastMappingDoesNotRewriteSlotLanguage(t *testing.T) {
	prior := Client
	defer func() { Client = prior }()
	var properties map[string]any
	if err := initClientWithTransport(esRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{}`
		if strings.HasSuffix(r.URL.Path, "/_mapping") {
			var request struct {
				Properties map[string]any `json:"properties"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			properties = request.Properties
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := EnsureBroadcastRetrievalFields(context.Background(), "items-*"); err != nil {
		t.Fatal(err)
	}
	slots := properties["retrieval_slots"].(map[string]any)
	fields := slots["properties"].(map[string]any)
	if len(fields) != 1 || fields["provider_region"].(map[string]any)["type"] != "keyword" {
		t.Fatalf("unexpected broadcast slot upgrade: %v", slots)
	}
	if _, changed := slots["dynamic"]; changed {
		t.Fatal("must preserve existing object dynamic policy")
	}
	lang := properties["lang"].(map[string]any)
	if lang["type"] != "text" || lang["fields"].(map[string]any)["keyword"].(map[string]any)["type"] != "keyword" {
		t.Fatalf("unexpected broadcast language mapping: %v", lang)
	}
}

func TestRetrievalMappingErrorsRemainFatalAndActionable(t *testing.T) {
	prior := Client
	defer func() { Client = prior }()
	if err := initClientWithTransport(esRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		status, body := 200, `{}`
		if strings.HasSuffix(r.URL.Path, "/_mapping") {
			status, body = 400, `{"error":{"reason":"mapper [retrieval_slots.provider_region] cannot be changed from type [text] to [keyword]"}}`
		}
		return &http.Response{StatusCode: status, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})); err != nil {
		t.Fatal(err)
	}
	for _, ensure := range []func(context.Context, ...string) error{EnsureBroadcastRetrievalFields, EnsureRetrievalSlots} {
		err := ensure(context.Background(), "conflicted-index")
		if err == nil || !strings.Contains(err.Error(), "conflicted-index") || !strings.Contains(err.Error(), "provider_region") || !strings.Contains(err.Error(), "HTTP 400") {
			t.Fatalf("missing mapping failure details: %v", err)
		}
	}
}

func TestBroadcastTemplateDefinesExactLanguageBeforeFirstWrite(t *testing.T) {
	properties := BuildIndexMapping(2)["properties"].(map[string]any)
	lang := properties["lang"].(map[string]any)
	if lang["type"] != "text" || lang["fields"].(map[string]any)["keyword"].(map[string]any)["type"] != "keyword" {
		t.Fatalf("rollover template omits exact language field: %v", lang)
	}
}
