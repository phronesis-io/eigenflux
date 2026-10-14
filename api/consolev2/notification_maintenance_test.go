package consolev2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

func validNotificationMaintenance() maintenanceEvent {
	return maintenanceEvent{EventID: strings.Repeat("a", 64), AttemptID: strings.Repeat("b", 32), EventAt: time.Now().UnixMilli(), Component: "notifier", Trigger: "auto", Phase: "notification", Result: "submitted", Platform: "windows", Source: "watch", FromVersion: "1.0.10", Notification: &notificationMaintenanceData{Action: "send", Visibility: "unknown", IDHash: strings.Repeat("c", 64), CLICommit: "271720ac"}}
}

func TestNotificationMaintenanceContract(t *testing.T) {
	now := time.Now()
	good := validNotificationMaintenance()
	if err := validateMaintenanceEvent(good, now); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*maintenanceEvent){
		func(e *maintenanceEvent) { e.Notification.Visibility = "seen" },
		func(e *maintenanceEvent) { e.Notification.IDHash = "order-123" },
		func(e *maintenanceEvent) { e.Notification.Stage = "private-path" },
		func(e *maintenanceEvent) { e.Notification.HResult = "token=secret" },
		func(e *maintenanceEvent) { e.Source = "unknown" },
		func(e *maintenanceEvent) { e.Result = "ready" },
		func(e *maintenanceEvent) { e.Phase = "uninstall" },
		func(e *maintenanceEvent) { e.ErrorCode = "owner@example.com" },
	} {
		e := validNotificationMaintenance()
		mutate(&e)
		if validateMaintenanceEvent(e, now) == nil {
			t.Fatal("unsafe notification claim accepted")
		}
	}
	for _, action := range []string{"setup", "status"} {
		e := validNotificationMaintenance()
		e.Source, e.Result, e.ErrorCode = "installer", "registered", "pending_first_notification"
		e.Notification.Action, e.Notification.IDHash = action, ""
		if err := validateMaintenanceEvent(e, now); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNotificationMaintenanceRejectsNestedPrivateFields(t *testing.T) {
	svc := &Service{telemetryRates: map[string]telemetryRateState{}}
	h := server.New()
	h.POST("/test", func(_ context.Context, c *app.RequestContext) { c.Set("agent_id", int64(42)) }, svc.recordMaintenanceBatch)
	e := map[string]interface{}{"event_id": strings.Repeat("a", 64), "event_at": time.Now().UnixMilli(), "attempt_id": strings.Repeat("b", 32), "component": "notifier", "trigger": "auto", "phase": "notification", "result": "submitted", "platform": "windows", "source": "watch", "notification": map[string]interface{}{"action": "send", "visibility": "unknown", "id_hash": strings.Repeat("c", 64), "url": "https://private.example"}}
	status, _, _ := performJSON(t, h, "POST", "/test", map[string]interface{}{"events": []interface{}{e}})
	if status != 400 {
		t.Fatalf("private nested field accepted: %d", status)
	}
}
