package cmd

import (
	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/desktopqueue"
	"cli.eigenflux.ai/internal/maintenance"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWatchNotificationTracePersistsAndUploadsWithoutPrivateContent(t *testing.T) {
	if !nativeDesktopSupported() {
		t.Skip("native notification platform required")
	}
	w := newCommissionWatch(t, "http://127.0.0.1:8090", "commission_order")
	w.emit = func(string, interface{}) error { return nil }
	trace := &maintenance.WatchNotification{Origin: "local_blocker", OrderID: "901", OrderVersion: 2, Role: "seller", JobID: strings.Repeat("a", 64), JobKind: "commission_order", JobStatus: "unknown", JobCode: "commission_intake_unconfirmed"}
	m := desktopnotify.Message{ID: "local:one", Title: "private title", Body: "private input token=secret", URL: "https://example.com/dashboard/notifications/open?order_id=901&role=seller"}
	path := filepath.Join(t.TempDir(), "desktop.json")
	q, err := desktopqueue.Open(path, w.scope)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.EnqueueWithTrace(m, trace); err != nil {
		t.Fatal(err)
	}
	trace.JobCode = "private error"
	q, err = desktopqueue.Open(path, w.scope)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := q.Next(time.Now())
	if !ok || entry.Watch.JobCode != "commission_intake_unconfirmed" {
		t.Fatalf("trace was not frozen: %+v", entry)
	}
	w.recordWatchNotification(entry, "queued", nil)
	w.recordWatchNotification(entry, "queued", nil)
	w.recordWatchNotification(entry, "prepare_failed", errors.New("private /path token=secret"))
	entry.Attempts = 1
	w.recordWatchNotification(entry, "os_failed", context.DeadlineExceeded)
	entry.Attempts = 2
	w.recordWatchNotification(entry, "os_submitted", nil)
	scope := w.maintenanceScope()
	if err = importNotificationObservations(scope); err != nil {
		t.Fatal(err)
	}
	if err = importNotificationObservations(scope); err != nil {
		t.Fatal(err)
	}
	var uploaded []maintenance.Event
	if err = maintenance.Flush(scope, time.Now(), func(events []maintenance.Event) error { uploaded = events; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(uploaded) != 4 {
		t.Fatalf("expected four stages, got %d", len(uploaded))
	}
	stages := map[string]int{}
	for _, event := range uploaded {
		if err = maintenance.Validate(event); err != nil {
			t.Fatal(err)
		}
		if event.Notification.Watch.JobID != strings.Repeat("a", 64) || event.Notification.Watch.OrderVersion != 2 || event.Notification.IDHash == "" {
			t.Fatalf("correlation missing: %+v", event)
		}
		stages[event.Notification.Stage] = event.Notification.Watch.Attempt
	}
	if stages["queued"] != 0 || stages["prepare_failed"] != 1 || stages["os_failed"] != 2 || stages["os_submitted"] != 3 {
		t.Fatalf("retry attempts lost: %v", stages)
	}
	raw, _ := json.Marshal(uploaded)
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "example.com") {
		t.Fatal("private notification content entered telemetry")
	}
	state, _ := maintenance.Snapshot(scope)
	if len(state.Events) != 0 {
		t.Fatal("acknowledged events remained queued")
	}
}

func TestWatchNotificationTelemetryDistinguishesJobsAndLegacy(t *testing.T) {
	if !nativeDesktopSupported() {
		t.Skip("native notification platform required")
	}
	w := newCommissionWatch(t, "http://127.0.0.1:8090", "commission_order")
	w.emit = func(string, interface{}) error { return nil }
	for i, id := range []string{"local:first", "local:second"} {
		meta := &maintenance.WatchNotification{Origin: "local_blocker", OrderID: "901", OrderVersion: int64(i + 2), Role: "seller", JobID: strings.Repeat(string(rune('a'+i)), 64), JobKind: "commission_order", JobStatus: "unknown", JobCode: "commission_intake_unconfirmed"}
		w.recordWatchNotification(desktopqueue.Entry{Message: desktopnotify.Message{ID: id}, Watch: meta}, "queued", nil)
	}
	w.recordWatchNotification(desktopqueue.Entry{Message: desktopnotify.Message{ID: "old", URL: "https://example.com/?order_id=901&role=seller"}}, "os_submitted", nil)
	logs, err := readNotificationObservations(w.home, w.server.Name)
	if err != nil || len(logs.Events) != 3 {
		t.Fatalf("lost distinct jobs: %+v %v", logs, err)
	}
	if logs.Events[0].NotificationIDHash == logs.Events[1].NotificationIDHash || logs.Events[0].Watch.JobID == logs.Events[1].Watch.JobID {
		t.Fatal("separate jobs collapsed")
	}
	legacy := logs.Events[2].Watch
	if legacy.Origin != "legacy" || legacy.OrderVersion != 0 || legacy.JobID != "" {
		t.Fatalf("guessed historical context: %+v", legacy)
	}
}

func TestWatchNotificationTelemetryFailureDoesNotChangeDesktopReceipt(t *testing.T) {
	if !nativeDesktopSupported() {
		t.Skip("native notification platform required")
	}
	w := newCommissionWatch(t, "http://127.0.0.1:8090", "commission_order")
	w.emit = func(string, interface{}) error { return nil }
	q, err := desktopqueue.Open(filepath.Join(t.TempDir(), "desktop.json"), w.scope)
	if err != nil {
		t.Fatal(err)
	}
	m := desktopnotify.Message{ID: "legacy:1", Title: "title", Body: "body", URL: "https://example.com/?order_id=901&role=seller"}
	if err = q.Enqueue(m); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(notificationLogPath(w.home, w.server.Name)), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(notificationLogPath(w.home, w.server.Name), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	entry, _ := q.Next(time.Now())
	w.recordWatchNotification(entry, "os_submitted", nil)
	if err = q.Complete(m.ID, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if pending, sent := q.Counts(); pending != 0 || sent != 1 {
		t.Fatalf("telemetry changed delivery: %d %d", pending, sent)
	}
}

func TestWatchNotificationCrashAfterSubmissionKeepsTwoReceipts(t *testing.T) {
	if !nativeDesktopSupported() {
		t.Skip("native notification platform required")
	}
	w := newCommissionWatch(t, "http://127.0.0.1:8090", "commission_order")
	w.emit = func(string, interface{}) error { return nil }
	path := filepath.Join(t.TempDir(), "desktop.json")
	q, err := desktopqueue.Open(path, w.scope)
	if err != nil {
		t.Fatal(err)
	}
	m := desktopnotify.Message{ID: "legacy:replayed", Title: "title", Body: "body", URL: "https://example.com/?order_id=901&role=seller"}
	if err = q.Enqueue(m); err != nil {
		t.Fatal(err)
	}
	first, _ := q.Next(time.Now())
	w.recordWatchNotification(first, "os_submitted", nil)
	if err = os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = q.Complete(m.ID, true, time.Now()); err == nil {
		t.Fatal("receipt write unexpectedly succeeded")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	reopened, err := desktopqueue.Open(path, w.scope)
	if err != nil {
		t.Fatal(err)
	}
	second, ok := reopened.Next(time.Now())
	if !ok || second.Attempts != first.Attempts {
		t.Fatal("failed delivery receipt changed retry state")
	}
	w.recordWatchNotification(second, "os_submitted", nil)
	if err = importNotificationObservations(w.maintenanceScope()); err != nil {
		t.Fatal(err)
	}
	state, err := maintenance.Snapshot(w.maintenanceScope())
	if err != nil || len(state.Events) != 2 {
		t.Fatalf("repeated physical submission disappeared: %+v %v", state.Events, err)
	}
	a, b := state.Events[0], state.Events[1]
	if a.EventID == b.EventID || a.AttemptID == b.AttemptID || a.Notification.IDHash != b.Notification.IDHash {
		t.Fatal("same notice needs distinct physical submission receipts")
	}
}
