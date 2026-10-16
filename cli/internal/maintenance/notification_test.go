package maintenance

import (
	"strings"
	"testing"
)

func TestNotificationEventContract(t *testing.T) {
	e := NewEvent(NewID(), "notifier", "auto", "notification", "submitted")
	e.Platform, e.Source = "windows", "watch"
	e.Notification = &Notification{Action: "send", Visibility: "unknown", IDHash: strings.Repeat("a", 64), CLICommit: "271720ac"}
	if err := Validate(e); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Event){
		func(e *Event) { e.Notification.Visibility = "shown" },
		func(e *Event) { e.Source = "unknown" },
		func(e *Event) { e.Notification.IDHash = "raw-id" },
		func(e *Event) { e.ErrorCode = "private token" },
		func(e *Event) { e.Phase = "install" },
	} {
		bad := e
		n := *e.Notification
		bad.Notification = &n
		mutate(&bad)
		if Validate(bad) == nil {
			t.Fatal("invalid notification accepted")
		}
	}
}

func TestNotificationDeviceContract(t *testing.T) {
	e := NewEvent(NewID(), "notifier", "auto", "notification", "registered")
	e.Platform, e.Source, e.ErrorCode = "windows", "watch", "pending_first_notification"
	e.Notification = &Notification{Action: "status", Visibility: "unknown", DeviceID: strings.Repeat("a", 32), DeviceName: "办公电脑"}
	if err := Validate(e); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Event){
		func(e *Event) { e.Notification.DeviceID = "" },
		func(e *Event) { e.Notification.DeviceName = "" },
		func(e *Event) { e.Notification.DeviceID = strings.Repeat("A", 32) },
		func(e *Event) { e.Notification.DeviceName = strings.Repeat("电", 65) },
		func(e *Event) { e.Notification.DeviceName = "computer\n" },
		func(e *Event) { e.Notification.DeviceName = "computer\u202e" },
		func(e *Event) { e.Notification.DeviceName = "computer\u2028other" },
		func(e *Event) { e.Notification.DeviceName = "computer\u2029other" },
		func(e *Event) { e.Notification.DeviceName = "\xff" },
		func(e *Event) { e.Notification.Action = "setup" },
	} {
		bad := e
		n := *e.Notification
		bad.Notification = &n
		mutate(&bad)
		if Validate(bad) == nil {
			t.Fatal("invalid notification device accepted")
		}
	}
	// Older clients do not send device metadata.
	e.Notification.DeviceID, e.Notification.DeviceName = "", ""
	if err := Validate(e); err != nil {
		t.Fatal(err)
	}
}
