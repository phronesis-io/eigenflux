package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/feedevent"
)

func TestDiscoveryFeedbackAttribution(t *testing.T) {
	var received []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		received = body.Items
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer server.Close()
	_, name := runtimeTestConfig(t, server.URL, true)
	cache.SaveFeedResponse(name, json.RawMessage(`{"impression_id":"search-A","items":[{"item_id":"9007199254740993","source_ref":{"type":"broadcast","id":"9007199254740993"}},{"item_id":"77","source_ref":{"type":"broadcast","id":"77"}}]}`))
	cache.SaveFeedResponse(name, json.RawMessage(`{"impression_id":"rec-B","items":[{"item_id":"9007199254740993","source_ref":{"type":"broadcast","id":"9007199254740993"}}]}`))
	ledger := feedevent.NewLedger(filepath.Join(cache.ServerDataDir(name), "broadcasts"), name)
	if _, status := ledger.LookupImpression("77", "search-A", time.Now().UnixMilli()); status != feedevent.StatusHit {
		t.Fatal("rapid discovery calls overwrote the earlier impression")
	}
	old, _ := feedFeedbackCmd.Flags().GetString("items")
	t.Cleanup(func() { _ = feedFeedbackCmd.Flags().Set("items", old) })
	_ = feedFeedbackCmd.Flags().Set("items", `[{"item_id":"9007199254740993","score":1},{"item_id":"77","score":2},{"item_id":"9007199254740993","score":0,"impression_id":"search-A"},{"item_id":"unknown","score":0}]`)
	if err := feedFeedbackCmd.RunE(feedFeedbackCmd, nil); err != nil {
		t.Fatal(err)
	}
	if len(received) != 4 {
		t.Fatal(received)
	}
	for i, want := range []string{"rec-B", "search-A", "search-A", ""} {
		got, _ := received[i]["impression_id"].(string)
		if got != want {
			t.Errorf("item %d impression=%q want %q", i, got, want)
		}
	}
}
