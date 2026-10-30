package desktopnotify

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNotificationObservationIsNotAVisibilityReceipt(t *testing.T) {
	message := Message{ID: "secret-notification-id", Title: "private text", Body: "private body", URL: "https://private.example/"}
	for _, tc := range []struct {
		action       string
		err          error
		result, code string
	}{
		{"enable", nil, "ready", ""},
		{"enable", ErrPendingFirstNotification, "registered", "pending_first_notification"},
		{"check", ErrPermission, "disabled", "permission_denied"},
		{"show", nil, "submitted", ""},
		{"show", &WindowsHelperError{Stage: "toast_show", HResult: "0x80070005"}, "failed", "provider_failed"},
		{"show", errors.New("private-provider-secret"), "failed", "provider_failed"},
	} {
		e := observeResult(tc.action, message, tc.err, time.Now())
		if e.Result != tc.result || e.Code != tc.code || e.Visibility != "unknown" {
			t.Fatalf("unexpected observation: %+v", e)
		}
		raw, _ := json.Marshal(e)
		for _, private := range []string{"secret-notification-id", "private text", "private body", "private.example", "private-provider-secret"} {
			if strings.Contains(string(raw), private) {
				t.Fatalf("private data in observation: %s", raw)
			}
		}
		if tc.action == "show" && len(e.NotificationIDHash) != 64 {
			t.Fatal("missing hashed notification ID")
		}
	}
}
