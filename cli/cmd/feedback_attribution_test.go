package cmd

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	if err := cache.SaveFeedResponse(name, json.RawMessage(`{"impression_id":"search-A","items":[{"item_id":"9007199254740993","source_ref":{"type":"broadcast","id":"9007199254740993"}},{"item_id":"77","source_ref":{"type":"broadcast","id":"77"}}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveFeedResponse(name, json.RawMessage(`{"impression_id":"rec-B","items":[{"item_id":"9007199254740993","source_ref":{"type":"broadcast","id":"9007199254740993"}}]}`)); err != nil {
		t.Fatal(err)
	}
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

func TestSurfaceUsesHandedPagesAndStripsLocalScope(t *testing.T) {
	var received []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Events []map[string]any `json:"events"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		received = body.Events
		w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer srv.Close()
	_, server := runtimeTestConfig(t, srv.URL, true)
	now := time.Now().UnixMilli()
	for _, page := range []struct {
		imp, items string
		handed     bool
	}{{"A", `[{"item_id":"1"}]`, true}, {"C", `[{"item_id":"2"}]`, true}, {"B", `[{"item_id":"1"},{"item_id":"2"}]`, false}} {
		raw := json.RawMessage(`{"schema_version":"feed.v2","impression_id":"` + page.imp + `","personalization":{"mode":"intent_aligned","context_revision":1},"items":` + page.items + `}`)
		if err := cache.SaveFeedResponse(server, raw); err != nil {
			t.Fatal(err)
		}
		if err := feedReportStore(server).Update(now, func(state *feedevent.Reports) {
			if _, err := recordFeedPage(state, raw, now, 0, false); err != nil {
				t.Fatal(err)
			}
			if !page.handed {
				for key, entry := range state.Entries {
					if entry.ImpressionID == page.imp {
						zero := int64(0)
						entry.HandedAt = &zero
						state.Entries[key] = entry
					}
				}
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	for flag, value := range map[string]string{"item-ids": "1,2", "kind": "surface", "impression-id": ""} {
		old, _ := feedEventRecordCmd.Flags().GetString(flag)
		t.Cleanup(func() { feedEventRecordCmd.Flags().Set(flag, old) })
		feedEventRecordCmd.Flags().Set(flag, value)
	}
	if err := feedEventRecordCmd.RunE(feedEventRecordCmd, nil); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 || received[0]["impression_id"] != "A" || received[1]["impression_id"] != "C" {
		t.Fatal(received)
	}
	for _, item := range received {
		if _, ok := item["_scope"]; ok {
			t.Fatal("local scope leaked", item)
		}
	}
}

func TestSurfaceReportsExpiredHandedItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"code":0,"data":{}}`)) }))
	defer srv.Close()
	_, server := runtimeTestConfig(t, srv.URL, true)
	now := time.Now().UnixMilli()
	for _, page := range []struct {
		imp, items string
		handed     bool
	}{{"old", `[{"item_id":"1"}]`, true}, {"unhanded", `[{"item_id":"2"}]`, false}} {
		raw := json.RawMessage(`{"schema_version":"feed.v2","impression_id":"` + page.imp + `","personalization":{"mode":"intent_aligned","context_revision":1},"items":` + page.items + `}`)
		if err := cache.SaveFeedResponse(server, raw); err != nil {
			t.Fatal(err)
		}
		if err := feedReportStore(server).Update(now, func(state *feedevent.Reports) {
			if _, err := recordFeedPage(state, raw, now, 0, false); err != nil {
				t.Fatal(err)
			}
			if !page.handed {
				for key, entry := range state.Entries {
					if entry.ImpressionID == page.imp {
						zero := int64(0)
						entry.HandedAt = &zero
						state.Entries[key] = entry
					}
				}
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Age the handed page's cache file past the ledger TTL.
	stale := time.Now().Add(-9 * 24 * time.Hour)
	if err := filepath.WalkDir(filepath.Join(cache.ServerDataDir(server), "broadcasts"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(path); strings.Contains(string(data), `"old"`) {
				return os.Chtimes(path, stale, stale)
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for flag, value := range map[string]string{"item-ids": "1,2", "kind": "surface", "impression-id": ""} {
		old, _ := feedEventRecordCmd.Flags().GetString(flag)
		t.Cleanup(func() { feedEventRecordCmd.Flags().Set(flag, old) })
		feedEventRecordCmd.Flags().Set(flag, value)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = write
	runErr := feedEventRecordCmd.RunE(feedEventRecordCmd, nil)
	os.Stdout = original
	_ = write.Close()
	out, _ := io.ReadAll(read)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(string(out), `"expired"`) || !strings.Contains(string(out), `"unknown_item"`) {
		t.Fatalf("output=%s", out)
	}
}
