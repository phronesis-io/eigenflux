package consolev2

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

func validWatchNotificationMaintenance() maintenanceEvent {
	e := validNotificationMaintenance()
	e.Notification.Action, e.Notification.Stage, e.Result = "watch", "queued", "queued"
	e.Notification.Watch = &watchNotificationMaintenanceData{Origin: "order_event", OrderID: "123", OrderVersion: 2, Role: "seller", SourceNotificationID: "456", Attempt: 0}
	return e
}

func TestWatchNotificationMaintenanceContract(t *testing.T) {
	now := time.Now()
	for _, origin := range []string{"order_event", "local_blocker", "legacy"} {
		for _, row := range []struct {
			stage, result, code string
			attempt             int
		}{
			{"queued", "queued", "", 0},
			{"prepare_failed", "failed", "service_name_unavailable", 1},
			{"os_submitted", "submitted", "", 2},
			{"os_failed", "failed", "provider_failed", 3},
		} {
			e := validWatchNotificationMaintenance()
			w := e.Notification.Watch
			w.Origin, w.Attempt = origin, row.attempt
			e.Notification.Stage, e.Result, e.ErrorCode = row.stage, row.result, row.code
			if origin != "order_event" {
				w.SourceNotificationID = ""
			}
			if origin == "local_blocker" {
				w.JobID, w.JobKind, w.JobStatus, w.JobCode = strings.Repeat("d", 64), "commission_order", "unknown", "commission_intake_unconfirmed"
			}
			if origin == "legacy" {
				w.OrderVersion = 0
			}
			if err := validateMaintenanceEvent(e, now); err != nil {
				t.Fatalf("%s/%s: %v", origin, row.stage, err)
			}
		}
	}
	for name, mutate := range map[string]func(*maintenanceEvent){
		"watch missing":         func(e *maintenanceEvent) { e.Notification.Watch = nil },
		"old action with watch": func(e *maintenanceEvent) { e.Notification.Action = "send" },
		"manual trigger":        func(e *maintenanceEvent) { e.Trigger = "manual" },
		"other source":          func(e *maintenanceEvent) { e.Source = "test" },
		"visibility":            func(e *maintenanceEvent) { e.Notification.Visibility = "seen" },
		"empty hash":            func(e *maintenanceEvent) { e.Notification.IDHash = "" },
		"raw id hash":           func(e *maintenanceEvent) { e.Notification.IDHash = "123" },
		"device metadata": func(e *maintenanceEvent) {
			e.Notification.DeviceID = strings.Repeat("d", 32)
			e.Notification.DeviceName = "Device"
		},
		"hresult":                func(e *maintenanceEvent) { e.Notification.HResult = "0x80070005" },
		"origin":                 func(e *maintenanceEvent) { e.Notification.Watch.Origin = "pm" },
		"zero order":             func(e *maintenanceEvent) { e.Notification.Watch.OrderID = "0" },
		"negative order":         func(e *maintenanceEvent) { e.Notification.Watch.OrderID = "-1" },
		"noncanonical order":     func(e *maintenanceEvent) { e.Notification.Watch.OrderID = "0123" },
		"overflow order":         func(e *maintenanceEvent) { e.Notification.Watch.OrderID = "9223372036854775808" },
		"zero version":           func(e *maintenanceEvent) { e.Notification.Watch.OrderVersion = 0 },
		"negative version":       func(e *maintenanceEvent) { e.Notification.Watch.OrderVersion = -1 },
		"role":                   func(e *maintenanceEvent) { e.Notification.Watch.Role = "owner" },
		"missing source id":      func(e *maintenanceEvent) { e.Notification.Watch.SourceNotificationID = "" },
		"noncanonical source id": func(e *maintenanceEvent) { e.Notification.Watch.SourceNotificationID = "+456" },
		"event has job":          func(e *maintenanceEvent) { e.Notification.Watch.JobID = strings.Repeat("e", 64) },
		"blocker missing job": func(e *maintenanceEvent) {
			e.Notification.Watch.Origin = "local_blocker"
			e.Notification.Watch.SourceNotificationID = ""
		},
		"legacy has job": func(e *maintenanceEvent) {
			e.Notification.Watch.Origin = "legacy"
			e.Notification.Watch.SourceNotificationID = ""
			e.Notification.Watch.JobCode = "execution_blocked"
		},
		"unknown stage":          func(e *maintenanceEvent) { e.Notification.Stage = "shown" },
		"queued attempt":         func(e *maintenanceEvent) { e.Notification.Watch.Attempt = 1 },
		"queued result":          func(e *maintenanceEvent) { e.Result = "submitted" },
		"queued error":           func(e *maintenanceEvent) { e.ErrorCode = "provider_failed" },
		"negative attempt":       func(e *maintenanceEvent) { e.Notification.Watch.Attempt = -1 },
		"submitted zero attempt": func(e *maintenanceEvent) { e.Notification.Stage = "os_submitted"; e.Result = "submitted" },
		"prepare wrong code": func(e *maintenanceEvent) {
			e.Notification.Stage = "prepare_failed"
			e.Result = "failed"
			e.ErrorCode = "provider_failed"
			e.Notification.Watch.Attempt = 1
		},
		"os failure no code": func(e *maintenanceEvent) {
			e.Notification.Stage = "os_failed"
			e.Result = "failed"
			e.Notification.Watch.Attempt = 1
		},
		"os failure preparation code": func(e *maintenanceEvent) {
			e.Notification.Stage = "os_failed"
			e.Result = "failed"
			e.ErrorCode = "service_name_unavailable"
			e.Notification.Watch.Attempt = 1
		},
		"commit unsafe": func(e *maintenanceEvent) { e.Notification.CLICommit = "/private/path" },
		"duration":      func(e *maintenanceEvent) { e.DurationMS = 60001 },
	} {
		t.Run(name, func(t *testing.T) {
			e := validWatchNotificationMaintenance()
			mutate(&e)
			if validateMaintenanceEvent(e, now) == nil {
				t.Fatal("invalid watch event accepted")
			}
		})
	}
	for _, field := range []string{"JobID", "JobKind", "JobStatus", "JobCode"} {
		e := validWatchNotificationMaintenance()
		w := e.Notification.Watch
		w.Origin, w.SourceNotificationID = "local_blocker", ""
		w.JobID, w.JobKind, w.JobStatus, w.JobCode = strings.Repeat("d", 64), "commission_fulfillment", "needs_user", "execution_blocked"
		switch field {
		case "JobID":
			w.JobID = "private"
		case "JobKind":
			w.JobKind = "pm"
		case "JobStatus":
			w.JobStatus = "completed"
		case "JobCode":
			w.JobCode = "raw_error_secret"
		}
		if validateMaintenanceEvent(e, now) == nil {
			t.Fatalf("invalid %s accepted", field)
		}
	}
}

func TestWatchNotificationMaintenanceRejectsNestedPrivateFields(t *testing.T) {
	svc := &Service{telemetryRates: map[string]telemetryRateState{}}
	h := server.New()
	h.POST("/test", func(_ context.Context, c *app.RequestContext) { c.Set("agent_id", int64(42)) }, svc.recordMaintenanceBatch)
	for _, field := range []string{"title", "body", "error", "session_id", "path", "agent_id"} {
		t.Run(field, func(t *testing.T) {
			raw, _ := json.Marshal(validWatchNotificationMaintenance())
			var e map[string]interface{}
			_ = json.Unmarshal(raw, &e)
			w := e["notification"].(map[string]interface{})["watch"].(map[string]interface{})
			w[field] = "private"
			status, _, _ := performJSON(t, h, "POST", "/test", map[string]interface{}{"events": []interface{}{e}})
			if status != 400 {
				t.Fatalf("private watch field accepted: %d", status)
			}
		})
	}
}

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
