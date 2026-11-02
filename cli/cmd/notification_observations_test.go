package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/maintenance"
	"github.com/spf13/cobra"
)

func TestNotificationStatusObservationsIncludeDeviceAcrossSources(t *testing.T) {
	for _, source := range []string{"manual", "watch", "test"} {
		t.Run(source, func(t *testing.T) {
			home := t.TempDir()
			legacy := notificationFixture("status", "42", source)
			if err := appendNotificationObservation(home, "eigenflux", legacy); err != nil {
				t.Fatal(err)
			}
			for _, action := range []string{"status", "setup", "send"} {
				event := notificationFixture(action, "untrusted-account", "untrusted-source")
				if err := recordNotificationObservationAt(home, "eigenflux", "42", source, event); err != nil {
					t.Fatal(err)
				}
			}
			device, err := notificationDeviceAt(home)
			if err != nil {
				t.Fatal(err)
			}
			hostname, _ := os.Hostname()
			scope := maintenance.Scope{Home: home, Server: "eigenflux", AgentID: "43"}
			if err := importNotificationObservations(scope); err != nil {
				t.Fatal(err)
			}
			other, err := maintenance.Snapshot(scope)
			if err != nil || len(other.Events) != 0 {
				t.Fatalf("observations crossed accounts: %+v %v", other, err)
			}
			scope.AgentID = "42"
			if err := importNotificationObservations(scope); err != nil {
				t.Fatal(err)
			}
			queued, err := maintenance.Snapshot(scope)
			if err != nil || len(queued.Events) != 4 {
				t.Fatalf("observations not queued: %+v %v", queued, err)
			}
			for _, event := range queued.Events {
				if err := maintenance.Validate(event); err != nil {
					t.Fatal(err)
				}
				if event.AttemptID == legacy.EventID || event.Notification.Action != "status" {
					if event.Notification.DeviceID != "" || event.Notification.DeviceName != "" {
						t.Fatalf("identity added to legacy or non-status event: %+v", event)
					}
					continue
				}
				if event.Notification.DeviceID != device.ID || event.Notification.DeviceName != notificationDeviceName(hostname, "windows") || event.Source != source {
					t.Fatalf("status lost scoped device metadata: %+v", event)
				}
			}
		})
	}
}

func TestNotificationStatusDoesNotReplaceUnreadableDeviceIdentity(t *testing.T) {
	home := t.TempDir()
	if _, err := notificationDeviceAt(home); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "notifications", "device.json")
	corrupt := []byte(`{"device_id":"invalid"}`)
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if err := recordNotificationObservationAt(home, "eigenflux", "42", "manual", notificationFixture("status", "42", "manual")); err == nil {
		t.Fatal("status recorded without a valid device identity")
	}
	state, err := readNotificationObservations(home, "eigenflux")
	if err != nil || len(state.Events) != 0 {
		t.Fatalf("incomplete status saved: %+v %v", state, err)
	}
	if err := recordNotificationObservationAt(home, "eigenflux", "42", "watch", notificationFixture("send", "42", "watch")); err != nil {
		t.Fatalf("device error blocked submission receipt: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, corrupt) {
		t.Fatalf("corrupt identity replaced: %s %v", got, err)
	}
}

func TestNotificationObservationsBoundAndIsolateLocalData(t *testing.T) {
	home := t.TempDir()
	path := notificationLogPath(home, "eigenflux")
	old := notificationObservations{Version: 1, Events: []desktopnotify.Observation{{At: time.Now().Add(-8 * 24 * time.Hour), Result: "expired"}}}
	if err := dispatch.WriteJSON(path, old); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxNotificationObservations+1; i++ {
		e := desktopnotify.Observation{At: time.Now(), Action: "send", Result: "submitted", Visibility: "unknown", Code: fmt.Sprint(i)}
		if err := appendNotificationObservation(home, "eigenflux", e); err != nil {
			t.Fatal(err)
		}
	}
	state, err := readNotificationObservations(home, "eigenflux")
	if err != nil || len(state.Events) != maxNotificationObservations || state.Events[0].Code != "1" {
		t.Fatalf("bounded observations: %+v %v", state, err)
	}
	other, err := readNotificationObservations(home, "other")
	if err != nil || len(other.Events) != 0 {
		t.Fatal("server observations leaked")
	}
}

func TestNotificationUnknownFirstUseIsRegisteredNotReady(t *testing.T) {
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := writeNotificationStatus(cmd, desktopnotify.ErrPendingFirstNotification); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "registered" || got["code"] != "pending_first_notification" || got["visibility"] != "unknown" {
		t.Fatal(got)
	}
}

func TestNotificationObservationStableEventIDPreservesOriginalReceipt(t *testing.T) {
	for _, status := range []string{"pending", "queued", "rejected"} {
		t.Run(status, func(t *testing.T) {
			home := t.TempDir()
			original := notificationFixture("send", "agent-1", "watch")
			original.Queued = status == "queued"
			if status == "rejected" {
				original.Rejected, original.RejectionCode = true, "invalid_observation"
			}
			if err := appendNotificationObservation(home, "eigenflux", original); err != nil {
				t.Fatal(err)
			}
			path := notificationLogPath(home, "eigenflux")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			duplicate := notificationFixture("send", "agent-1", "watch")
			duplicate.EventID = original.EventID
			duplicate.At = original.At.Add(time.Minute)
			for i := 0; i < 2; i++ {
				if err := appendNotificationObservation(home, "eigenflux", duplicate); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("duplicate replaced original receipt or status: %s %v", after, err)
			}
		})
	}
}
