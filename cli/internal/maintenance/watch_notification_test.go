package maintenance

import (
	"strings"
	"testing"
)

func TestInputRequestNotificationCorrelation(t *testing.T) {
	valid := func() Event {
		e := NewEvent(NewID(), "notifier", "auto", "notification", "queued")
		e.Source, e.Platform = "watch", "darwin"
		e.Notification = &Notification{Action: "watch", Visibility: "unknown", IDHash: strings.Repeat("a", 64), Stage: "queued", Watch: &WatchNotification{
			Origin: "input_request", Role: "buyer", OrderID: "123", OrderVersion: 2,
			JobID: strings.Repeat("d", 64), JobKind: "commission_clarification", InputRequestIDHash: strings.Repeat("e", 64), InputRound: 2,
		}}
		return e
	}
	if err := Validate(valid()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*WatchNotification){
		"seller":              func(w *WatchNotification) { w.Role = "seller" },
		"zero version":        func(w *WatchNotification) { w.OrderVersion = 0 },
		"no job":              func(w *WatchNotification) { w.JobID = "" },
		"wrong job":           func(w *WatchNotification) { w.JobKind = "commission_order" },
		"source notification": func(w *WatchNotification) { w.SourceNotificationID = "456" },
		"status":              func(w *WatchNotification) { w.JobStatus = "needs_user" },
		"code":                func(w *WatchNotification) { w.JobCode = "execution_blocked" },
		"raw input":           func(w *WatchNotification) { w.InputRequestIDHash = "private question" },
		"no round":            func(w *WatchNotification) { w.InputRound = 0 },
		"extra round":         func(w *WatchNotification) { w.InputRound = 4 },
		"wrong origin":        func(w *WatchNotification) { w.Origin = "local_blocker" },
	} {
		t.Run(name, func(t *testing.T) {
			e := valid()
			mutate(e.Notification.Watch)
			if Validate(e) == nil {
				t.Fatal("invalid input request notification accepted")
			}
		})
	}
}
