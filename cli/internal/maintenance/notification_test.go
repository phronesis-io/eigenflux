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
