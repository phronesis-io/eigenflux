package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/dispatch"
	"github.com/spf13/cobra"
)

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
