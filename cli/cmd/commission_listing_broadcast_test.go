package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
)

func TestListingBroadcastSkipKeepsModerationACKOnTelemetryFailure(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			var acks []map[string]string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v2/notifications/ack" || r.Header.Get("X-EigenFlux-Notification-Receipts") != `{"82":"execution_enqueued"}` {
					t.Errorf("changed moderation ACK request: %s %s", r.Method, r.URL.Path)
				}
				var body struct {
					Notifications []map[string]string `json:"notifications"`
				}
				dec := json.NewDecoder(r.Body)
				dec.DisallowUnknownFields()
				if err := dec.Decode(&body); err != nil {
					t.Error(err)
				}
				acks = append(acks, body.Notifications...)
				io.WriteString(w, `{"code":0,"data":{}}`)
			}))
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			scope := w.maintenanceScope().ForListingBroadcast()
			if corrupt {
				d, _ := listingBroadcastRef("90:1")
				if err := maintenance.Record(scope, listingBroadcastEvent(d, maintenance.NewID(), "skipped", "setting_off")); err != nil {
					t.Fatal(err)
				}
				files, err := filepath.Glob(filepath.Join(w.home, "maintenance", "*-listing-broadcast.json"))
				if err != nil || len(files) != 1 {
					t.Fatalf("missing isolated journal: %v %v", files, err)
				}
				if err := os.WriteFile(files[0], []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			notice := moderationTestNotification("42", "published")
			parsed, err := parseModerationNotification(notice, "42")
			if err != nil {
				t.Fatal(err)
			}
			if err := w.deliverCommissionNotifications(context.Background(), commissionTestPage(notice, commissionTestNotification(82, 901, "42")), "http_poll"); err != nil {
				t.Fatal(err)
			}
			wantACK := []map[string]string{
				{"notification_id": fmt.Sprint(parsed.ID), "source_type": moderationNotificationSource},
				{"notification_id": "82", "source_type": "commission_order"},
			}
			if !reflect.DeepEqual(acks, wantACK) {
				t.Fatalf("ACK changed: %v", acks)
			}
			jobs := w.journal.Snapshot()
			if len(jobs) != 1 || jobs[0].Kind != "commission_order" {
				t.Fatalf("order not queued: %+v", jobs)
			}
			if !corrupt {
				state, err := maintenance.Snapshot(scope)
				if err != nil || len(state.Events) != 1 || state.Events[0].Result != "skipped" || state.Events[0].ListingBroadcast.Reason != "main_binding_unavailable" {
					t.Fatalf("missing queue skip: %+v, %v", state.Events, err)
				}
			}
		})
	}
}

func TestListingPublishUsesUnchangedRequestAndRecordsAPIResult(t *testing.T) {
	for _, tc := range []struct {
		name, flag, response, result, reason, itemID string
	}{
		{"normal", "", `{"code":0,"data":{"item_id":"201"}}`, "", "", ""},
		{"listing", "9007199254740993:2", `{"code":0,"data":{"item_id":"201"}}`, "submitted", "", "201"},
		{"rejected", "9007199254740993:2", `{"code":400,"msg":"original error"}`, "failed", "rejected", ""},
		{"no item ID", "9007199254740993:2", `{"code":0,"data":{}}`, "unconfirmed", "missing_broadcast_id", ""},
		{"broken response", "9007199254740993:2", `broken`, "unconfirmed", "request_unconfirmed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/items/publish") {
					io.WriteString(w, `{"code":0,"data":{}}`)
					return
				}
				calls++
				var body map[string]any
				want := map[string]any{"content": "c", "notes": "{}", "accept_reply": true}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(want, body) {
					t.Errorf("publish request changed: %v %v", body, err)
				}
				io.WriteString(w, tc.response)
			}))
			defer api.Close()
			_, name := runtimeTestConfig(t, api.URL, true)
			credentials, err := auth.LoadV2Credentials(name)
			if err != nil {
				t.Fatal(err)
			}
			resetPublishFlagsForTest(t)
			args := []string{"publish", "--content", "c", "--notes", "{}"}
			if tc.flag != "" {
				args = append(args, "--commission-listing", tc.flag)
			}
			_, err = runArgs(t, args...)
			if (tc.result == "failed" || tc.reason == "request_unconfirmed") != (err != nil) || calls != 1 {
				t.Fatalf("unexpected business result: %v, %d requests", err, calls)
			}
			scope := maintenance.Scope{Home: config.HomeDir(), Server: name, AgentID: credentials.AgentID}
			state, err := maintenance.Snapshot(scope.ForListingBroadcast())
			if err != nil {
				t.Fatal(err)
			}
			if tc.result == "" {
				if len(state.Events) != 0 {
					t.Fatal("ordinary publish recorded as listing")
				}
				return
			}
			if len(state.Events) != 2 || state.Events[0].Result != "started" || state.Events[0].AttemptID != state.Events[1].AttemptID {
				t.Fatalf("missing correlated start and receipt: %+v", state.Events)
			}
			event := state.Events[1]
			if d := event.ListingBroadcast; event.Result != tc.result || d.Reason != tc.reason || d.BroadcastID != tc.itemID ||
				d.CommissionID != "9007199254740993" || d.PublicRevision != 2 {
				t.Fatalf("wrong receipt: %+v %+v", event, d)
			}
		})
	}
}

func TestListingTelemetryUploadsFromHeartbeatWithoutBackingOffRegularMaintenance(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Events []json.RawMessage `json:"events"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Events) != 1 {
			t.Errorf("mixed or invalid batch: %+v %v", req, err)
			http.Error(w, "mixed batch", 400)
			return
		}
		if strings.Contains(string(req.Events[0]), `"listing_broadcast"`) {
			requests = append(requests, "listing")
			if r.URL.Path != "/api/v2/maintenance/listing-broadcasts:batch" {
				t.Errorf("listing sent to legacy endpoint: %s", r.URL.Path)
			}
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/api/v2/maintenance/events:batch" {
			t.Errorf("ordinary event sent to listing endpoint: %s", r.URL.Path)
		}
		requests = append(requests, "regular")
		io.WriteString(w, `{"code":0,"data":{"accepted_events":1}}`)
	}))
	defer server.Close()
	_, name := runtimeTestConfig(t, server.URL, true)
	credentials, err := auth.LoadV2Credentials(name)
	if err != nil {
		t.Fatal(err)
	}
	scope := maintenance.Scope{Home: config.HomeDir(), Server: name, AgentID: credentials.AgentID}
	detail, _ := listingBroadcastRef("90:1")
	observeListingBroadcast(scope, detail, maintenance.NewID(), "skipped", "setting_off")
	flushRegular := func() {
		t.Helper()
		if err := maintenance.Record(scope, maintenance.NewEvent(maintenance.NewID(), "cli", "auto", "check", "started")); err != nil {
			t.Fatal(err)
		}
		sent, err := flushMaintenanceBatch(scope, credentials.PrincipalID, server.URL)
		if err != nil || len(sent) != 1 || sent[0].ListingBroadcast != nil {
			t.Fatalf("regular maintenance upload changed: %+v %v", sent, err)
		}
	}
	flushRegular()
	if !reflect.DeepEqual(requests, []string{"regular"}) {
		t.Fatalf("shared CLI update path attempted a listing upload: %v", requests)
	}
	flushHeartbeatListingBroadcastMaintenance(name)
	flushRegular()
	if !reflect.DeepEqual(requests, []string{"regular", "listing", "regular"}) {
		t.Fatalf("heartbeat skipped listing or old backend blocked regular telemetry: %v", requests)
	}
	regular, _ := maintenance.Snapshot(scope)
	listing, _ := maintenance.Snapshot(scope.ForListingBroadcast())
	if len(regular.Events) != 0 || regular.Failures != 0 || regular.NextFlush != 0 ||
		len(listing.Events) != 1 || listing.Failures != 1 || listing.NextFlush == 0 {
		t.Fatalf("backoff not isolated: regular=%+v listing=%+v", regular, listing)
	}
}

func TestListingBroadcastUploadChecksEndpointAndReceipt(t *testing.T) {
	for _, scenario := range []string{"stale endpoint", "unconfirmed receipt", "confirmed receipt"} {
		t.Run(scenario, func(t *testing.T) {
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if scenario == "confirmed receipt" {
					w.WriteHeader(http.StatusAccepted)
				}
				io.WriteString(w, `{"code":0,"data":{"accepted_events":1}}`)
			}))
			defer server.Close()
			endpoint := server.URL
			if scenario == "stale endpoint" {
				endpoint = server.URL + "/changed"
			}
			_, name := runtimeTestConfig(t, endpoint, true)
			credentials, err := auth.LoadV2Credentials(name)
			if err != nil {
				t.Fatal(err)
			}
			scope := maintenance.Scope{Home: config.HomeDir(), Server: name, AgentID: credentials.AgentID}
			detail, _ := listingBroadcastRef("90:1")
			observeListingBroadcast(scope, detail, maintenance.NewID(), "skipped", "setting_off")
			flushListingBroadcastMaintenance(scope, credentials.PrincipalID, server.URL, credentials)
			state, err := maintenance.Snapshot(scope.ForListingBroadcast())
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "stale endpoint":
				if calls != 0 || len(state.Events) != 1 {
					t.Fatalf("stale endpoint got %d requests, %d events remain", calls, len(state.Events))
				}
			case "unconfirmed receipt":
				if calls != 1 || len(state.Events) != 1 {
					t.Fatalf("HTTP 200 removed unconfirmed events: %d %d", calls, len(state.Events))
				}
			case "confirmed receipt":
				if calls != 1 || len(state.Events) != 0 {
					t.Fatalf("HTTP 202 failed to remove events: %d %d", calls, len(state.Events))
				}
			}
		})
	}
}

func TestListingBroadcastSkipCommandRecordsOnlyKnownReason(t *testing.T) {
	_, name := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	credentials, err := auth.LoadV2Credentials(name)
	if err != nil {
		t.Fatal(err)
	}
	if flushesFeedQueue(commissionObserveBroadcastSkipCmd) {
		t.Fatal("local skip observation must not flush unrelated feed feedback")
	}
	for _, reason := range []string{"setting_off", "raw owner@example.com error"} {
		_, err := runArgs(t, "commission", "observe-broadcast-skip", "90:2", reason)
		if (reason == "setting_off") != (err == nil) {
			t.Fatalf("skip reason %q: %v", reason, err)
		}
	}
	scope := maintenance.Scope{Home: config.HomeDir(), Server: name, AgentID: credentials.AgentID}
	state, err := maintenance.Snapshot(scope.ForListingBroadcast())
	if err != nil || len(state.Events) != 1 {
		t.Fatalf("skip journal: %+v %v", state.Events, err)
	}
	event := state.Events[0]
	if event.Result != "skipped" || event.ListingBroadcast.Reason != "setting_off" {
		t.Fatalf("wrong skip: %+v", event)
	}
}
