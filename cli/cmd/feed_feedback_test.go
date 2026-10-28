package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/feedevent"
)

func feedPollTestServer(t *testing.T, feedCalls *int, feedback *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/agent-context":
			fmt.Fprint(w, `{"code":0,"data":{"context_revision":1,"control_context":{"context_revision":1,"intent_actions":[]}}}`)
		case "/api/v2/runtime/heartbeat":
			fmt.Fprint(w, `{"code":0,"data":{}}`)
		case "/api/v2/feed":
			*feedCalls++
			fmt.Fprintf(w, `{"code":0,"data":{"schema_version":"feed.v2","impression_id":"imp-%d","output_contract":"score before presenting","personalization":{"mode":"intent_aligned","context_revision":1},"items":[{"item_id":"9007199254740993","source_ref":{"type":"broadcast"},"preview":{"text":"Useful signal"}}]}}`, *feedCalls)
		case "/api/v2/items/feedback":
			if err := json.NewDecoder(r.Body).Decode(feedback); err != nil {
				t.Error(err)
			}
			fmt.Fprint(w, `{"code":0,"data":{"processed_count":1,"results":[{"item_id":"9007199254740993","status":"accepted"}]}}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
}

func pollFeedForTest(t *testing.T, name string) {
	t.Helper()
	oldOrigin, oldFormat := originFlag, formatFlag
	t.Cleanup(func() { originFlag = oldOrigin; formatFlag = oldFormat })
	originFlag, formatFlag = "heartbeat", "json"
	if _, err := captureHeartbeatStdout(t, func() error { return pollFeedV2(feedPollCmd, name, "20") }); err != nil {
		t.Fatal(err)
	}
}

func TestScheduledPullAlwaysFetchesAndFeedbackClearsPending(t *testing.T) {
	feedCalls := 0
	var feedback map[string]any
	srv := feedPollTestServer(t, &feedCalls, &feedback)
	defer srv.Close()
	_, name := runtimeTestConfig(t, srv.URL, true)
	pollFeedForTest(t, name)
	pollFeedForTest(t, name)
	if feedCalls != 2 {
		t.Fatal(feedCalls)
	}
	state, err := feedReportStore(name).Read()
	if err != nil {
		t.Fatal(err)
	}
	pending := pendingFeed(name, state, time.Now().UnixMilli())
	if len(pending) != 2 || pending[0].ImpressionID != "imp-1" || pending[1].ImpressionID != "imp-2" {
		t.Fatal(pending)
	}
	previous, _ := feedFeedbackCmd.Flags().GetString("items")
	defer feedFeedbackCmd.Flags().Set("items", previous)
	_ = feedFeedbackCmd.Flags().Set("items", `[{"item_id":"9007199254740993","impression_id":"imp-1","score":2,"surfaced":true}]`)
	if err := submitDurableFeedback(feedFeedbackCmd, nil); err != nil {
		t.Fatal(err)
	}
	sent := feedback["items"].([]any)[0].(map[string]any)
	if sent["impression_id"] != "imp-1" || sent["impression_source"] != "explicit" || feedback["contract_version"] != "feedback.v2" || len(sent["client_key"].(string)) != 32 {
		t.Fatal(feedback)
	}
	state, _ = feedReportStore(name).Read()
	pending = pendingFeed(name, state, time.Now().UnixMilli())
	if len(pending) != 1 || pending[0].ImpressionID != "imp-2" {
		t.Fatal(pending)
	}
}

// The ledger is diagnostic: an unreadable file must never block Feed or
// feedback delivery, and the next write starts a fresh ledger.
func TestCorruptReportLedgerNeverBlocksFeedOrFeedback(t *testing.T) {
	feedCalls := 0
	var feedback map[string]any
	srv := feedPollTestServer(t, &feedCalls, &feedback)
	defer srv.Close()
	_, name := runtimeTestConfig(t, srv.URL, true)
	dir := cache.ServerDataDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(dir, "feed-reports.json")
	if err := os.WriteFile(ledger, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	pollFeedForTest(t, name)
	if feedCalls != 1 {
		t.Fatal(feedCalls)
	}
	quarantined, _ := filepath.Glob(ledger + ".corrupt-*")
	if len(quarantined) != 1 {
		t.Fatal(quarantined)
	}
	state, err := feedReportStore(name).Read()
	if err != nil || len(pendingFeed(name, state, time.Now().UnixMilli())) != 1 {
		t.Fatal(state, err)
	}
	if err := os.WriteFile(ledger, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous, _ := feedFeedbackCmd.Flags().GetString("items")
	defer feedFeedbackCmd.Flags().Set("items", previous)
	_ = feedFeedbackCmd.Flags().Set("items", `[{"item_id":"9007199254740993","impression_id":"imp-1","score":1}]`)
	if err := submitDurableFeedback(feedFeedbackCmd, nil); err != nil {
		t.Fatal(err)
	}
	if feedback == nil {
		t.Fatal("feedback not delivered")
	}
	depth, _ := feedevent.NewQueue(filepath.Join(dir, "events")).Stats(time.Now().UnixMilli())
	if depth != 0 {
		t.Fatal(depth)
	}
}

func TestFeedStatusBoundsAndFeedbackValidation(t *testing.T) {
	items := make([]feedevent.PendingItem, 25)
	for i := range items {
		items[i] = feedevent.PendingItem{ItemID: fmt.Sprint(i), AgeMs: int64(100 - i)}
	}
	status := buildFeedStatus(items)
	if len(status.Items) != 20 || status.Pending != 25 || status.OldestAgeMs != 100 {
		t.Fatal(status)
	}
	ledger := feedevent.NewLedger(t.TempDir(), "server")
	for _, raw := range []string{`[]`, `[{"item_id":"1","score":1.5}]`, `[{"item_id":"1","score":3}]`, `[{"item_id":"1","score":1,"surfaced":"true"}]`} {
		if _, err := prepareFeedback(raw, ledger, feedevent.Reports{}, "scope", time.Now().UnixMilli()); err == nil {
			t.Fatal(raw)
		}
	}
}
func TestDurableFeedbackFailureNextInvocationAndHealth(t *testing.T) {
	fail := true
	keys := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		keys = append(keys, body.Items[0]["client_key"].(string))
		if fail {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"results":[{"item_id":"1","status":"rejected","reason":"own_item"}]}}`)
	}))
	defer srv.Close()
	cfg, name := runtimeTestConfig(t, srv.URL, true)
	previous, _ := feedFeedbackCmd.Flags().GetString("items")
	defer feedFeedbackCmd.Flags().Set("items", previous)
	_ = feedFeedbackCmd.Flags().Set("items", `[{"item_id":"1","score":1}]`)
	if err := submitDurableFeedback(feedFeedbackCmd, nil); err != nil {
		t.Fatal(err)
	}
	q := feedevent.NewQueue(filepath.Join(cache.ServerDataDir(name), "events"))
	depth, _ := q.Stats(time.Now().UnixMilli())
	if depth != 1 {
		t.Fatal(depth)
	}
	fail = false
	maybeFlushFeedQueue()
	depth, _ = q.Stats(time.Now().UnixMilli())
	if depth != 0 || len(keys) != 2 || keys[0] != keys[1] {
		t.Fatal(depth, keys)
	}
	counts := reportedFeedbackHealth(cfg, name)
	if counts["scored_items"] != 1 || counts["feedback_attempts"] != 2 || counts["feedback_failures"] != 1 || counts["window_ms"] != feedevent.HealthWindowMs {
		t.Fatal(counts)
	}
}

func TestFeedbackReplayRestoresJudgmentAfterQueueOnlyWrite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"results":[{"item_id":"1","status":"accepted"}]}}`)
	}))
	defer srv.Close()
	_, name := runtimeTestConfig(t, srv.URL, true)
	now := time.Now().UnixMilli()
	store := feedReportStore(name)
	for _, prior := range []bool{false, true} {
		t.Run(fmt.Sprint(prior), func(t *testing.T) {
			if err := store.Update(now, func(r *feedevent.Reports) {
				r.Entries = map[string]feedevent.Report{}
				r.Health = nil
				if prior {
					r.RecordFeedback(feedevent.Event{"item_id": "1", "client_key": "old", "ts": now - 100, "score": 0}, now, "accepted")
				}
			}); err != nil {
				t.Fatal(err)
			}
			events, err := prepareFeedback(`[{"item_id":"1","score":2}]`, feedevent.NewLedger(t.TempDir(), name), feedevent.Reports{}, feedReportScope(name), now-50)
			if err != nil {
				t.Fatal(err)
			}
			// This is the exact persisted state left by a process stopping after
			// Enqueue and before the separate report-ledger write.
			if err := feedevent.NewQueue(filepath.Join(cache.ServerDataDir(name), "events")).Enqueue(events); err != nil {
				t.Fatal(err)
			}
			if _, remaining, err := flushFeedQueue(name, nil); err != nil || remaining != 0 {
				t.Fatal(remaining, err)
			}
			if err := store.Count(now, map[string]int64{"pulls": 1}); err != nil {
				t.Fatal(err)
			}
			state, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Entries) != 1 {
				t.Fatal(state)
			}
			for _, entry := range state.Entries {
				if entry.Score != 2 || entry.ClientKey != events[0]["client_key"] || entry.ScoredAt != now-50 || entry.Delivery != "accepted" {
					t.Fatal(entry)
				}
			}
			want := int64(1)
			if prior {
				want = 2
			}
			if state.HealthCounts(now)["scored_items"] != want {
				t.Fatal(state.HealthCounts(now))
			}
		})
	}
}

func TestFeedbackRequiresAuthenticatedAgentBeforeEnqueue(t *testing.T) {
	_, name := runtimeTestConfig(t, "http://127.0.0.1:1", false)
	if err := auth.DeleteCredentials(name); err != nil {
		t.Fatal(err)
	}
	previous, _ := feedFeedbackCmd.Flags().GetString("items")
	defer feedFeedbackCmd.Flags().Set("items", previous)
	_ = feedFeedbackCmd.Flags().Set("items", `[{"item_id":"1","score":1}]`)
	if err := submitDurableFeedback(feedFeedbackCmd, nil); err == nil {
		t.Fatal("unauthenticated feedback succeeded")
	}
	depth, _ := feedevent.NewQueue(filepath.Join(cache.ServerDataDir(name), "events")).Stats(time.Now().UnixMilli())
	if depth != 0 {
		t.Fatal(depth)
	}
}

func TestForegroundFeedbackRefreshesExpiredCredentials(t *testing.T) {
	refreshes, submits := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/agent-sessions/refresh-challenges":
			fmt.Fprint(w, `{"data":{"nonce":"nonce","issued_at":123}}`)
		case "/api/v2/agent-sessions/refresh":
			refreshes++
			fmt.Fprintf(w, `{"data":{"agent_id":"agent-1","access_token":"fresh","refresh_token":"next","expires_at":%d}}`, time.Now().Add(time.Hour).UnixMilli())
		case "/api/v2/items/feedback":
			submits++
			if r.Header.Get("Authorization") != "Bearer fresh" {
				t.Error("feedback used expired credentials")
			}
			fmt.Fprint(w, `{"code":0,"data":{"results":[{"item_id":"1","status":"accepted"}]}}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	_, name := runtimeTestConfig(t, srv.URL, true)
	creds, err := auth.LoadV2Credentials(name)
	if err != nil {
		t.Fatal(err)
	}
	creds.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
	if err := auth.SaveV2Credentials(name, creds); err != nil {
		t.Fatal(err)
	}
	previous, _ := feedFeedbackCmd.Flags().GetString("items")
	defer feedFeedbackCmd.Flags().Set("items", previous)
	_ = feedFeedbackCmd.Flags().Set("items", `[{"item_id":"1","score":1}]`)
	if err := submitDurableFeedback(feedFeedbackCmd, nil); err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 || submits != 1 {
		t.Fatal(refreshes, submits)
	}
}

func TestExpiredCredentialRefreshOutageKeepsFeedbackDurable(t *testing.T) {
	outage := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/agent-sessions/refresh-challenges":
			if outage {
				w.WriteHeader(503)
				return
			}
			fmt.Fprint(w, `{"data":{"nonce":"nonce","issued_at":123}}`)
		case "/api/v2/agent-sessions/refresh":
			fmt.Fprintf(w, `{"data":{"agent_id":"agent-1","access_token":"fresh","refresh_token":"next","expires_at":%d}}`, time.Now().Add(time.Hour).UnixMilli())
		case "/api/v2/items/feedback":
			if r.Header.Get("Authorization") != "Bearer fresh" {
				w.WriteHeader(401)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"results":[{"item_id":"1","status":"accepted"}]}}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	_, name := runtimeTestConfig(t, srv.URL, true)
	creds, _ := auth.LoadV2Credentials(name)
	creds.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
	if err := auth.SaveV2Credentials(name, creds); err != nil {
		t.Fatal(err)
	}
	previous, _ := feedFeedbackCmd.Flags().GetString("items")
	defer feedFeedbackCmd.Flags().Set("items", previous)
	_ = feedFeedbackCmd.Flags().Set("items", `[{"item_id":"1","score":1}]`)
	if err := submitDurableFeedback(feedFeedbackCmd, nil); err != nil {
		t.Fatal(err)
	}
	queue := feedevent.NewQueue(filepath.Join(cache.ServerDataDir(name), "events"))
	if depth, _ := queue.Stats(time.Now().UnixMilli()); depth != 1 {
		t.Fatal(depth)
	}
	outage = false
	if err := feedEventFlushCmd.RunE(feedEventFlushCmd, nil); err != nil {
		t.Fatal(err)
	}
	if depth, _ := queue.Stats(time.Now().UnixMilli()); depth != 0 {
		t.Fatal(depth)
	}
}

func TestLifecycleCommandsDoNotFlushFeedQueue(t *testing.T) {
	for _, args := range [][]string{{"uninstall"}, {"skills", "uninstall"}, {"uninstall-finalize"}, {"installation"}, {"migrate"}, {"version"}} {
		cmd, _, err := rootCmd.Find(args)
		if err != nil || cmd == rootCmd {
			t.Fatalf("%v not found: %v", args, err)
		}
		if flushesFeedQueue(cmd) {
			t.Errorf("%v flushes the Feed queue", args)
		}
	}
	for _, args := range [][]string{{"feed", "poll"}, {"feed", "feedback"}, {"heartbeat", "plan"}, {"settings", "push"}} {
		cmd, _, err := rootCmd.Find(args)
		if err != nil || !flushesFeedQueue(cmd) {
			t.Errorf("%v must flush the Feed queue: %v", args, err)
		}
	}
}
