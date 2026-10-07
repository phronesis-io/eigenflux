package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFeedEventPushSendsExactNumericID(t *testing.T) {
	for _, source := range []string{"inline", "batch"} {
		t.Run(source, func(t *testing.T) {
			var received struct {
				Events []struct {
					ItemID   string `json:"item_id"`
					DedupKey string `json:"dedup_key"`
				} `json:"events"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/items/events" {
					t.Errorf("unexpected request path: %s", r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"code":0,"data":{"accepted":1}}`))
			}))
			defer server.Close()
			runtimeTestConfig(t, server.URL, true)
			flags := feedEventPushCmd.Flags()
			oldItems, _ := flags.GetString("items")
			oldBatch, _ := flags.GetString("batch")
			t.Cleanup(func() {
				_ = flags.Set("items", oldItems)
				_ = flags.Set("batch", oldBatch)
			})
			body := `[{"item_id":9007199254740993,"kind":"surface","impression_id":"synthetic"}]`
			inline, batch := body, ""
			if source == "batch" {
				inline, batch = "", filepath.Join(t.TempDir(), "events.json")
				if err := os.WriteFile(batch, []byte(`{"events":`+body+`}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := flags.Set("items", inline); err != nil {
				t.Fatal(err)
			}
			if err := flags.Set("batch", batch); err != nil {
				t.Fatal(err)
			}
			if err := feedEventPushCmd.RunE(feedEventPushCmd, nil); err != nil {
				t.Fatal(err)
			}
			if len(received.Events) != 1 || received.Events[0].ItemID != "9007199254740993" {
				t.Fatalf("request changed the item ID: %+v", received)
			}
			if received.Events[0].DedupKey != feedEventDedupKey("agent-1", "9007199254740993", "surface", "synthetic") {
				t.Fatalf("request has incorrect dedup identity: %+v", received)
			}
		})
	}
}

func TestFeedEventNumericIDsRemainExact(t *testing.T) {
	for _, id := range []string{"123", "9007199254740992", "9007199254740993", "366084135833305089", "9223372036854775807"} {
		for _, source := range []string{"inline", "batch", "builder"} {
			t.Run(id+"/"+source, func(t *testing.T) {
				body := fmt.Sprintf(`[{"item_id":%s,"kind":"surface","impression_id":"synthetic"}]`, id)
				var events []map[string]interface{}
				var err error
				if source == "builder" {
					events, err = buildFeedEvents(body, "42")
				} else {
					inline, batch := body, ""
					if source == "batch" {
						inline, batch = "", filepath.Join(t.TempDir(), "events.json")
						if err := os.WriteFile(batch, []byte(`{"events":`+body+`}`), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					items, parseErr := parseFeedEventItems(inline, batch)
					if parseErr != nil {
						t.Fatal(parseErr)
					}
					events, err = buildFeedEventsFromItems(items, "42")
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := events[0]["item_id"]; got != id {
					t.Fatalf("item_id = %v, want %s", got, id)
				}
				quoted, err := buildFeedEvents(fmt.Sprintf(`[{"item_id":%q,"kind":"surface","impression_id":"synthetic"}]`, id), "42")
				if err != nil {
					t.Fatal(err)
				}
				if events[0]["dedup_key"] != quoted[0]["dedup_key"] {
					t.Fatalf("numeric and string IDs have different dedup identities: %v vs %v", events, quoted)
				}
			})
		}
	}
}

func TestFeedEventsRejectInvalidItemIDs(t *testing.T) {
	for _, id := range []string{"0", "-1", "1.5", "1e3", "9223372036854775808", `"9223372036854775808"`, `"not-an-id"`, "true", "null"} {
		t.Run(id, func(t *testing.T) {
			if _, err := buildFeedEvents(fmt.Sprintf(`[{"item_id":%s,"kind":"surface"}]`, id), "42"); err == nil {
				t.Fatal("expected invalid positive int64 item ID to be rejected")
			}
		})
	}
}

func TestFeedEventInputsRejectTrailingJSON(t *testing.T) {
	for _, body := range []string{`[{"item_id":1,"kind":"surface"}] []`, `[{"item_id":1,"kind":"surface"}] junk`} {
		if _, err := parseFeedEventItems(body, ""); err == nil {
			t.Fatalf("inline input accepted trailing JSON: %s", body)
		}
		if _, err := buildFeedEvents(body, "42"); err == nil {
			t.Fatalf("builder accepted trailing JSON: %s", body)
		}
	}
	batch := filepath.Join(t.TempDir(), "events.json")
	if err := os.WriteFile(batch, []byte(`{"events":[{"item_id":1,"kind":"surface"}]} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseFeedEventItems("", batch); err == nil {
		t.Fatal("batch input accepted trailing JSON")
	}
}
