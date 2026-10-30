package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/maintenance"
)

func TestNotificationDeviceIdentityPersistsAndRejectsCorruption(t *testing.T) {
	home := t.TempDir()
	first, err := notificationDeviceAt(home)
	if err != nil || !notificationDeviceIDPattern.MatchString(first.ID) {
		t.Fatalf("initial identity: %+v %v", first, err)
	}
	second, err := notificationDeviceAt(home)
	if err != nil || second != first {
		t.Fatalf("identity changed on reuse: %+v %v", second, err)
	}
	other, err := notificationDeviceAt(t.TempDir())
	if err != nil || other.ID == first.ID {
		t.Fatalf("independent installation identity: %+v %v", other, err)
	}
	if err := os.WriteFile(filepath.Join(home, "notifications", "device.json"), []byte(`{"device_id":"invalid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := notificationDeviceAt(home); err == nil {
		t.Fatal("corrupt identity must not be silently replaced")
	}
}

func TestNotificationDeviceNameIsSafeAndBounded(t *testing.T) {
	for _, tt := range []struct{ input, platform, want string }{
		{"  Lynn\n\x00\u202e的\u2028电\u2029脑  ", "darwin", "Lynn的电脑"},
		{strings.Repeat("电", 80), "windows", strings.Repeat("电", 64)},
		{"\xff", "windows", "Windows PC"},
		{" ", "darwin", "Mac"},
	} {
		if got := notificationDeviceName(tt.input, tt.platform); got != tt.want {
			t.Fatalf("device name: %q, want %q", got, tt.want)
		}
	}
}

func TestNotificationPermissionRecordsChangesRenewalsAndPinnedAccount(t *testing.T) {
	home := t.TempDir()
	device, err := notificationDeviceAt(home)
	if err != nil {
		t.Fatal(err)
	}
	var state notificationPermissionState
	base := time.Now().UTC().Add(-10 * time.Minute)
	observation := desktopnotify.Observation{At: base, Platform: "windows", Action: "status", Result: "registered", Code: "pending_first_notification", Visibility: "unknown"}
	record := func(at time.Time, result, code string) {
		t.Helper()
		observation.At, observation.Result, observation.Code = at, result, code
		if err := state.record(home, "eigenflux", "42", device, "Windows PC", observation); err != nil {
			t.Fatal(err)
		}
	}
	record(base, "registered", "pending_first_notification")
	record(base.Add(time.Minute), "registered", "pending_first_notification")
	record(base.Add(5*time.Minute), "registered", "pending_first_notification")
	record(base.Add(6*time.Minute), "disabled", "permission_denied")
	record(base.Add(7*time.Minute), "ready", "")
	logs, err := readNotificationObservations(home, "eigenflux")
	if err != nil || len(logs.Events) != 4 {
		t.Fatalf("wanted startup, renewal, denied, recovery only: %+v %v", logs, err)
	}
	if logs.Events[0].Result != "registered" || logs.Events[0].Code != "pending_first_notification" {
		t.Fatal("unknown Windows first-use state became a permission denial")
	}
	for _, event := range logs.Events {
		if event.DeviceID != device.ID || event.DeviceName != "Windows PC" || event.AgentID != "42" || event.Source != "watch" {
			t.Fatalf("missing identity: %+v", event)
		}
	}
	other := maintenance.Scope{Home: home, Server: "eigenflux", AgentID: "99"}
	if err := importNotificationObservations(other); err != nil {
		t.Fatal(err)
	}
	snapshot, err := maintenance.Snapshot(other)
	if err != nil || len(snapshot.Events) != 0 {
		t.Fatalf("permission reassigned to other account: %+v %v", snapshot, err)
	}
	scope := maintenance.Scope{Home: home, Server: "eigenflux", AgentID: "42"}
	if err := importNotificationObservations(scope); err != nil {
		t.Fatal(err)
	}
	snapshot, err = maintenance.Snapshot(scope)
	if err != nil || len(snapshot.Events) != 4 {
		t.Fatalf("permission events not queued: %+v %v", snapshot, err)
	}
	for _, event := range snapshot.Events {
		if err := maintenance.Validate(event); err != nil || event.Notification.DeviceID != device.ID || event.Notification.DeviceName != "Windows PC" {
			t.Fatalf("wire contract: %+v %v", event, err)
		}
	}
}

func TestNotificationPermissionQueueFailureDoesNotSuppressRetry(t *testing.T) {
	home := t.TempDir()
	device, err := notificationDeviceAt(home)
	if err != nil {
		t.Fatal(err)
	}
	var state notificationPermissionState
	observation := desktopnotify.Observation{At: time.Now(), Platform: "darwin", Action: "status", Result: "failed", Code: "permission_required", Visibility: "unknown"}
	path := notificationLogPath(home, "eigenflux")
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := state.record(home, "eigenflux", "42", device, "Mac", observation); err == nil {
		t.Fatal("corrupt queue accepted")
	}
	if !state.recordedAt.IsZero() {
		t.Fatal("failed save advanced the renewal clock")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := state.record(home, "eigenflux", "42", device, "Mac", observation); err != nil {
		t.Fatal(err)
	}
}
