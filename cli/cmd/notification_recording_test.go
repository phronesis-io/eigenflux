package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"cli.eigenflux.ai/internal/config"
	"github.com/spf13/cobra"
)

func TestNotificationRecordingFailureCannotClaimReported(t *testing.T) {
	for _, backlog := range []bool{false, true} {
		name := "empty_queue"
		if backlog {
			name = "older_receipt_uploaded"
		}
		t.Run(name, func(t *testing.T) {
			var uploads atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/maintenance/events:batch" || r.Header.Get("Authorization") != "Bearer test-v2" {
					t.Errorf("unexpected upload: %s", r.URL.Path)
				}
				uploads.Add(1)
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"code":0,"data":{"accepted_events":1}}`))
			}))
			defer srv.Close()
			_, server := runtimeTestConfig(t, srv.URL, true)
			home := config.HomeDir()
			if _, err := notificationDeviceAt(home); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "notifications", "device.json")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			corrupt := []byte(`{"device_id":"invalid"}`)
			if err := os.WriteFile(path, corrupt, 0600); err != nil {
				t.Fatal(err)
			}
			if backlog {
				if err := appendNotificationObservation(home, server, notificationFixture("setup", "agent-1", "manual")); err != nil {
					t.Fatal(err)
				}
			}

			notifier := observedNotificationCLI()
			// Exercise the real observation callback without invoking native OS UI.
			notifier.observe(home, server, "agent-1", "manual", notificationFixture("status", "agent-1", "manual"))
			telemetry := notifier.flushTelemetry(server)
			var output bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&output)
			if err := writeNotificationStatus(cmd, nil, telemetry); err != nil {
				t.Fatal(err)
			}
			var result map[string]string
			if err := json.Unmarshal(output.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result["status"] != "ready" || result["telemetry"] != "record_failed" {
				t.Fatalf("recording failure rewrote OS result or claimed upload: %s", output.String())
			}
			expected := int32(0)
			if backlog {
				expected = 1
			}
			logs, err := readNotificationObservations(home, server)
			if err != nil || len(logs.Events) != int(expected) || uploads.Load() != expected {
				t.Fatalf("lost older receipt or saved incomplete status: logs=%+v uploads=%d err=%v", logs, uploads.Load(), err)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, corrupt) {
				t.Fatalf("invalid identity was replaced: %q %v", got, err)
			}

			// A later successful invocation reports independently after identity repair.
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			recovered := observedNotificationCLI()
			recovered.observe(home, server, "agent-1", "manual", notificationFixture("status", "agent-1", "manual"))
			if got := recovered.flushTelemetry(server); got != "reported" {
				t.Fatalf("recovered observation did not upload: %s", got)
			}
			logs, err = readNotificationObservations(home, server)
			if err != nil || len(logs.Events) != int(expected)+1 || uploads.Load() != expected+1 || logs.Events[len(logs.Events)-1].DeviceID == "" {
				t.Fatalf("recovery receipt missing device or upload: %+v uploads=%d err=%v", logs, uploads.Load(), err)
			}
		})
	}
}
