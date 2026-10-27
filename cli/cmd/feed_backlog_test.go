package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/feedevent"
)

func TestHoldFeedBacklogDecision(t *testing.T) {
	pending := []feedevent.PendingItem{{AgeMs: 1000}}
	for _, test := range []struct {
		name, origin, action, mode string
		baseline                   bool
		age                        int64
		holds                      int
		empty, want                bool
	}{
		{name: "hold", origin: "heartbeat", mode: "hold", age: 1000, want: true},
		{name: "refresh", origin: "heartbeat", action: "refresh", mode: "hold", age: 1000, want: true},
		{name: "owner", origin: "owner", mode: "hold", age: 1000},
		{name: "absent origin", mode: "hold", age: 1000},
		{name: "more", origin: "heartbeat", action: "more", mode: "hold", age: 1000},
		{name: "off", origin: "heartbeat", mode: "off", age: 1000},
		{name: "baseline", origin: "heartbeat", mode: "hold", baseline: true, age: 1000},
		{name: "fourth pull", origin: "heartbeat", mode: "hold", holds: 3, age: 1000},
		{name: "six hours", origin: "heartbeat", mode: "hold", age: feedBacklogMaxAgeMs},
		{name: "empty", origin: "heartbeat", mode: "hold", empty: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			items := pending
			items[0].AgeMs = test.age
			if test.empty {
				items = nil
			}
			got := holdFeedBacklog(test.origin, test.action, test.mode, test.baseline, items, test.holds)
			if got != test.want {
				t.Fatal(got)
			}
		})
	}
}
func TestBacklogPollNotificationAttributionAndStarvation(t *testing.T) {
	feedCalls, noticeCalls := 0, 0
	var feedback map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/agent-context":
			fmt.Fprint(w, `{"code":0,"data":{"context_revision":1,"control_context":{"context_revision":1,"intent_actions":[]}}}`)
		case "/api/v2/runtime/heartbeat":
			fmt.Fprint(w, `{"code":0,"data":{}}`)
		case "/api/v2/feed":
			feedCalls++
			fmt.Fprintf(w, `{"code":0,"data":{"schema_version":"feed.v2","impression_id":"imp-%d","output_contract":"score before presenting","personalization":{"mode":"intent_aligned","context_revision":1},"items":[{"item_id":"9007199254740993","source_ref":{"type":"broadcast"},"preview":{"text":"Useful signal"}}]}}`, feedCalls)
		case "/api/v2/notifications/pending":
			noticeCalls++
			fmt.Fprint(w, `{"code":0,"data":{"notifications":[{"notification_id":"notice"}]}}`)
		case "/api/v2/items/feedback":
			if err := json.NewDecoder(r.Body).Decode(&feedback); err != nil {
				t.Error(err)
			}
			fmt.Fprint(w, `{"code":0,"data":{"processed_count":1,"results":[{"item_id":"9007199254740993","status":"accepted"}]}}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	cfg, name := runtimeTestConfig(t, srv.URL, true)
	if err := cfg.SetServerKV(name, "feed_backlog_mode", "hold"); err != nil {
		t.Fatal(err)
	}
	oldOrigin, oldFormat := originFlag, formatFlag
	t.Cleanup(func() { originFlag = oldOrigin; formatFlag = oldFormat; feedPollCmd.SetOut(nil) })
	originFlag = "heartbeat"
	formatFlag = "json"
	poll := func() map[string]any {
		t.Helper()
		raw, err := captureHeartbeatStdout(t, func() error { return pollFeedV2(feedPollCmd, name, "20") })
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatal(raw, err)
		}
		return value
	}
	poll()
	if feedCalls != 1 {
		t.Fatal(feedCalls)
	}
	if err := feedReportStore(name).Update(time.Now().UnixMilli(), func(r *feedevent.Reports) {
		r.RecordSurface(feedevent.Event{"item_id": "9007199254740993", "impression_id": "imp-1"}, time.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	held := poll()
	items := held["items"].([]any)
	item := items[0].(map[string]any)
	if feedCalls != 1 || noticeCalls != 1 || held["backlog"].(map[string]any)["held"] != true || item["impression_id"] != "imp-1" || item["already_surfaced"] != true || len(held["notifications"].([]any)) != 1 {
		t.Fatal(held, feedCalls, noticeCalls)
	}
	raw, _ := json.Marshal(held)
	formatFlag = "agent"
	var rendered bytes.Buffer
	feedPollCmd.SetOut(&rendered)
	if err := renderFeedV2(feedPollCmd, raw); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rendered.String(), "BACKLOG: 1 items") || !strings.Contains(rendered.String(), "score before presenting") {
		t.Fatal(rendered.String())
	}
	formatFlag = "json"
	poll()
	poll()
	poll()
	if feedCalls != 2 || noticeCalls != 3 {
		t.Fatal(feedCalls, noticeCalls)
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
	state, _ := feedReportStore(name).Read()
	pending := pendingFeed(name, state, time.Now().UnixMilli())
	if len(pending) != 1 || pending[0].ImpressionID != "imp-2" {
		t.Fatal(pending)
	}
	if err := cfg.SetServerKV(name, "feed_backlog_mode", "off"); err != nil {
		t.Fatal(err)
	}
	poll()
	if feedCalls != 3 {
		t.Fatal(feedCalls)
	}
}
func TestFeedStatusBoundsAndFeedbackValidation(t *testing.T) {
	items := make([]feedevent.PendingItem, 25)
	for i := range items {
		items[i] = feedevent.PendingItem{ItemID: fmt.Sprint(i), AgeMs: int64(100 - i)}
	}
	status := buildFeedStatus(items, 2)
	raw, _ := json.Marshal(status)
	if len(status.Items) != 20 || status.Pending != 25 || status.OldestAgeMs != 100 || !strings.Contains(string(raw), `"held_cycles":2`) {
		t.Fatal(status)
	}
	ledger := feedevent.NewLedger(t.TempDir(), "server")
	for _, raw := range []string{`[]`, `[{"item_id":"1","score":1.5}]`, `[{"item_id":"1","score":3}]`, `[{"item_id":"1","score":1,"surfaced":"true"}]`} {
		if _, err := prepareFeedback(raw, ledger, "scope", time.Now().UnixMilli()); err == nil {
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
	_ = feedReportStore(name).Count(time.Now().UnixMilli(), map[string]int64{"backlog_holds": 1, "backlog_releases": 1})
	if _, ok := reportedFeedbackHealth(cfg, name)["backlog_holds"]; ok {
		t.Fatal("unsupported health fields sent")
	}
	if err := cfg.SetServerKV(name, "feedback_health_backlog_supported", "true"); err != nil {
		t.Fatal(err)
	}
	if reportedFeedbackHealth(cfg, name)["backlog_holds"] != 1 {
		t.Fatal("advertised counters missing")
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
			events, err := prepareFeedback(`[{"item_id":"1","score":2}]`, feedevent.NewLedger(t.TempDir(), name), feedReportScope(name), now-50)
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
