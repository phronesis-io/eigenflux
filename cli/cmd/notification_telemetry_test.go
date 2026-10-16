package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/watch"
)

func notificationFixture(action, agent, source string) desktopnotify.Observation {
	e := desktopnotify.Observation{EventID: maintenance.NewID(), AgentID: agent, Source: source, At: time.Now().UTC(), Platform: "windows", Action: action, Result: "ready", Visibility: "unknown", CLIVersion: "1.0.10", CLICommit: "271720ac"}
	if action == "send" {
		e.Result = "submitted"
		e.NotificationIDHash = strings.Repeat("a", 64)
	}
	return e
}

func TestNotificationTelemetryImportsInstallationAndPinsAccounts(t *testing.T) {
	home := t.TempDir()
	for _, e := range []desktopnotify.Observation{
		notificationFixture("setup", "", "installer"),
		notificationFixture("send", "agent-1", "watch"),
		notificationFixture("send", "agent-2", "watch"),
		notificationFixture("send", "", "manual"),
	} {
		if err := appendNotificationObservation(home, "eigenflux", e); err != nil {
			t.Fatal(err)
		}
	}
	scope := maintenance.Scope{Home: home, Server: "eigenflux", AgentID: "agent-1"}
	for i := 0; i < 2; i++ {
		if err := importNotificationObservations(scope); err != nil {
			t.Fatal(err)
		}
	}
	state, err := maintenance.Snapshot(scope)
	if err != nil || len(state.Events) != 2 {
		t.Fatalf("import/dedup: %+v %v", state, err)
	}
	for _, e := range state.Events {
		if err := maintenance.Validate(e); err != nil {
			t.Fatal(err)
		}
	}
	scope.AgentID = "agent-2"
	if err := importNotificationObservations(scope); err != nil {
		t.Fatal(err)
	}
	other, err := maintenance.Snapshot(scope)
	if err != nil || len(other.Events) != 1 || other.Events[0].Notification.Action != "send" {
		t.Fatal("account events reassigned")
	}
}

func TestNotificationTelemetryRejectsInvalidLegacyObservationWithoutBlockingReceipts(t *testing.T) {
	home := t.TempDir()
	bad := notificationFixture("send", "agent-1", "watch")
	bad.CLICommit = "276005e6-fallback"
	good := notificationFixture("send", "agent-1", "watch")
	other := bad
	other.EventID, other.AgentID = maintenance.NewID(), "agent-2"
	unbound := bad
	unbound.EventID, unbound.AgentID = maintenance.NewID(), ""
	for _, e := range []desktopnotify.Observation{bad, good, other, unbound} {
		if err := appendNotificationObservation(home, "eigenflux", e); err != nil {
			t.Fatal(err)
		}
	}
	scope := maintenance.Scope{Home: home, Server: "eigenflux", AgentID: "agent-1"}
	for i := 0; i < 2; i++ {
		if err := importNotificationObservations(scope); err != nil {
			t.Fatal(err)
		}
	}
	state, err := readNotificationObservations(home, "eigenflux")
	if err != nil || len(state.Events) != 4 {
		t.Fatalf("original observations must remain: %+v %v", state, err)
	}
	expectedBad := bad
	expectedBad.Rejected, expectedBad.RejectionCode = true, "invalid_observation"
	if !reflect.DeepEqual(state.Events[0], expectedBad) {
		t.Fatalf("rejection modified original fields or claimed success: %+v", state.Events[0])
	}
	if !state.Events[1].Queued || state.Events[1].Rejected {
		t.Fatalf("valid receipt was blocked: %+v", state.Events[1])
	}
	if !reflect.DeepEqual(state.Events[2], other) || !reflect.DeepEqual(state.Events[3], unbound) {
		t.Fatal("invalid records from another account or an unbound send were touched")
	}
	queued, err := maintenance.Snapshot(scope)
	if err != nil || len(queued.Events) != 1 || queued.Events[0].AttemptID != good.EventID {
		t.Fatalf("only one valid receipt should be queued: %+v %v", queued, err)
	}
	// notify log encodes this same state, including its fixed rejection reason.
	raw, err := json.Marshal(state)
	if err != nil || !strings.Contains(string(raw), `"rejection_code":"invalid_observation"`) {
		t.Fatalf("rejection is not locally diagnosable: %s %v", raw, err)
	}
}

func TestNotificationTelemetryWriteFailureKeepsReceiptPendingForRetry(t *testing.T) {
	home := t.TempDir()
	bad := notificationFixture("send", "agent-1", "watch")
	bad.CLICommit = "276005e6-fallback"
	good := notificationFixture("send", "agent-1", "watch")
	for _, e := range []desktopnotify.Observation{bad, good} {
		if err := appendNotificationObservation(home, "eigenflux", e); err != nil {
			t.Fatal(err)
		}
	}
	// A real filesystem error prevents the maintenance queue from being created.
	blocked := filepath.Join(home, "maintenance")
	if err := os.WriteFile(blocked, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	scope := maintenance.Scope{Home: home, Server: "eigenflux", AgentID: "agent-1"}
	if err := importNotificationObservations(scope); err == nil {
		t.Fatal("temporary queue storage error was ignored")
	}
	state, err := readNotificationObservations(home, "eigenflux")
	if err != nil || !state.Events[0].Rejected || !reflect.DeepEqual(state.Events[1], good) {
		t.Fatalf("rejection must persist while valid receipt stays pending: %+v %v", state, err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := importNotificationObservations(scope); err != nil {
			t.Fatal(err)
		}
	}
	queued, err := maintenance.Snapshot(scope)
	if err != nil || len(queued.Events) != 1 || queued.Events[0].AttemptID != good.EventID {
		t.Fatalf("retry must queue the valid receipt exactly once: %+v %v", queued, err)
	}
}

func TestNotificationTelemetryBusyQueueKeepsReceiptPendingForRetry(t *testing.T) {
	home := t.TempDir()
	event := notificationFixture("send", "agent-1", "watch")
	if err := appendNotificationObservation(home, "eigenflux", event); err != nil {
		t.Fatal(err)
	}
	scope := maintenance.Scope{Home: home, Server: "eigenflux", AgentID: "agent-1"}
	if _, err := maintenance.Snapshot(scope); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(home, "maintenance", "*-events.json.lock"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("maintenance lock fixture: %v %v", paths, err)
	}
	release, err := watch.AcquirePath(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			release()
		}
	})
	if err := importNotificationObservations(scope); !errors.Is(err, maintenance.ErrBusy) {
		t.Fatalf("lock failure must remain retryable: %v", err)
	}
	state, err := readNotificationObservations(home, "eigenflux")
	if err != nil || len(state.Events) != 1 || !reflect.DeepEqual(state.Events[0], event) {
		t.Fatalf("temporary lock failure incorrectly changed receipt: %+v %v", state, err)
	}
	release()
	released = true
	if err := importNotificationObservations(scope); err != nil {
		t.Fatal(err)
	}
	queued, err := maintenance.Snapshot(scope)
	if err != nil || len(queued.Events) != 1 || queued.Events[0].AttemptID != event.EventID {
		t.Fatalf("receipt did not retry after queue unlocked: %+v %v", queued, err)
	}
}

func TestNotificationTelemetryHTTPFailureRetainsQueueAndRetryUsesSameIDs(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var calls atomic.Int32
	var eventIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/maintenance/events:batch" || r.Header.Get("Authorization") != "Bearer test-v2" {
			t.Errorf("wrong authenticated transport: %s", r.URL.Path)
		}
		var body struct {
			Events []maintenance.Event `json:"events"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Events) != 1 {
			t.Errorf("unexpected batch size: %d", len(body.Events))
		} else {
			e := body.Events[0]
			if err := maintenance.Validate(e); err != nil {
				t.Error(err)
			}
			if e.Source != "installer" || e.Notification.Action != "setup" || e.Notification.Visibility != "unknown" {
				t.Error("incorrect installation event")
			}
			eventIDs = append(eventIDs, e.EventID)
		}
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(500)
			w.Write([]byte(`{"code":1,"msg":"unavailable"}`))
			return
		}
		w.WriteHeader(202)
		w.Write([]byte(`{"code":0,"msg":"ok","data":{"accepted_events":1}}`))
	}))
	defer srv.Close()
	_, server := runtimeTestConfig(t, srv.URL, true)
	e := notificationFixture("setup", "", "installer")
	if err := appendNotificationObservation(config.HomeDir(), server, e); err != nil {
		t.Fatal(err)
	}
	if status := flushNotificationTelemetry(server); status != "queued" {
		t.Fatalf("upload failure lost: %s", status)
	}
	scope, err := maintenanceScope(server)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := maintenance.Snapshot(scope)
	if len(state.Events) != 1 {
		t.Fatal("failed upload removed pending event")
	}
	fail.Store(false)
	// Advance the queue clock beyond backoff; avoid sleeping in a unit test.
	if err := maintenance.Flush(scope, time.Now().Add(time.Minute), func(events []maintenance.Event) error {
		c, _, err := newV2ClientForServer(server, true)
		if err != nil {
			return err
		}
		_, err = c.Post("/maintenance/events:batch", map[string]interface{}{"events": events})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if status := flushNotificationTelemetry(server); status != "reported" {
		t.Fatalf("acknowledgement not reflected: %s", status)
	}
	state, _ = maintenance.Snapshot(scope)
	if len(state.Events) != 0 || calls.Load() != 2 || eventIDs[0] != eventIDs[1] {
		t.Fatal("retry replayed observation or did not clear acknowledgement")
	}
}
