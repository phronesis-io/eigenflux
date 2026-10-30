package consolev2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

func validCommissionMaintenance() maintenanceEvent {
	e := validMaintenanceEvent()
	e.Component, e.Phase, e.Result, e.Source, e.Platform = "dispatch", "commission", "started", "watch", "darwin"
	e.ToVersion, e.RunningVersion = "", ""
	e.Commission = &commissionMaintenanceData{OrderID: "123", OrderVersion: 2, Role: "seller", JobID: strings.Repeat("c", 64), JobKind: "commission_fulfillment", Stage: "agent", Attempt: 1}
	return e
}

func TestCommissionMaintenanceStagesAndSafeDiagnostics(t *testing.T) {
	now := time.Now()
	for _, stage := range strings.Fields("claimed preflight agent artifact upload_grant upload_put upload_confirm deliver verify main_notice input_check accept job_finished retry") {
		for _, result := range []string{"started", "succeeded", "failed", "blocked", "retry_scheduled", "skipped"} {
			e := validCommissionMaintenance()
			e.Commission.Stage, e.Result = stage, result
			if maintenanceOneOf(result, "failed", "blocked", "retry_scheduled") {
				e.ErrorCode = "transport_failed"
			}
			if result == "retry_scheduled" {
				e.Commission.NextAttemptAt = now.Add(time.Minute).UnixMilli()
			}
			if err := validateMaintenanceEvent(e, now); err != nil {
				t.Fatalf("%s/%s: %v", stage, result, err)
			}
		}
	}
	for _, transport := range []string{"", "socket", "http_poll", "local"} {
		e := validCommissionMaintenance()
		e.Commission.Transport = transport
		if err := validateMaintenanceEvent(e, now); err != nil {
			t.Fatalf("transport %s: %v", transport, err)
		}
	}
	eNotice := validCommissionMaintenance()
	eNotice.Commission.JobKind, eNotice.Commission.InputRequestIDHash = "main_notice", strings.Repeat("f", 64)
	if err := validateMaintenanceEvent(eNotice, now); err != nil {
		t.Fatalf("main notice without round: %v", err)
	}
	e := validCommissionMaintenance()
	e.Commission.Stage, e.Commission.OrderVersion, e.Result, e.ErrorCode = "preflight", 0, "failed", "preflight_unavailable"
	if err := validateMaintenanceEvent(e, now); err != nil {
		t.Fatalf("first order read failure cannot be reported: %v", err)
	}
	e.Commission.Stage, e.Commission.OrderVersion, e.Commission.Attempt, e.Result, e.ErrorCode = "queued", 2, 0, "queued", ""
	if err := validateMaintenanceEvent(e, now); err != nil {
		t.Fatal(err)
	}
}

func TestCommissionMaintenanceRejectsUnsafeClaims(t *testing.T) {
	for name, mutate := range map[string]func(*maintenanceEvent){
		"missing metadata":     func(e *maintenanceEvent) { e.Commission = nil },
		"other phase":          func(e *maintenanceEvent) { e.Phase = "install" },
		"notifier metadata":    func(e *maintenanceEvent) { e.Notification = &notificationMaintenanceData{} },
		"identity":             func(e *maintenanceEvent) { e.Commission.OrderID = "00123" },
		"unread version":       func(e *maintenanceEvent) { e.Commission.OrderVersion = 0 },
		"private transport":    func(e *maintenanceEvent) { e.Commission.Transport = "https://private-token" },
		"unknown stage":        func(e *maintenanceEvent) { e.Commission.Stage = "a private prompt" },
		"private job":          func(e *maintenanceEvent) { e.Commission.JobID = "/Users/private" },
		"private session":      func(e *maintenanceEvent) { e.Commission.HostSessionIDHash = "/private/session" },
		"private error":        func(e *maintenanceEvent) { e.Result, e.ErrorCode = "failed", "token-secret" },
		"unclassified failure": func(e *maintenanceEvent) { e.Result = "failed" },
		"successful error":     func(e *maintenanceEvent) { e.Result, e.ErrorCode = "succeeded", "timeout" },
		"bad status":           func(e *maintenanceEvent) { e.Commission.HTTPStatus = 600 },
		"bad attempt":          func(e *maintenanceEvent) { e.Commission.Attempt = 6 },
		"zero attempt":         func(e *maintenanceEvent) { e.Commission.Attempt = 0 },
		"unscheduled time":     func(e *maintenanceEvent) { e.Commission.NextAttemptAt = e.EventAt + 1000 },
		"retry in past": func(e *maintenanceEvent) {
			e.Result, e.ErrorCode, e.Commission.NextAttemptAt = "retry_scheduled", "timeout", e.EventAt-1
		},
		"retry exhausted": func(e *maintenanceEvent) {
			e.Result, e.ErrorCode, e.Commission.NextAttemptAt, e.Commission.Attempt = "retry_scheduled", "timeout", e.EventAt+1000, 5
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := validCommissionMaintenance()
			mutate(&e)
			if err := validateMaintenanceEvent(e, time.Now()); err == nil {
				t.Fatal("invalid diagnostic accepted")
			}
		})
	}
}

func TestCommissionMaintenanceHTTPRejectsRawProperties(t *testing.T) {
	svc := &Service{telemetryRates: map[string]telemetryRateState{}}
	h := server.New()
	h.POST("/test", func(_ context.Context, c *app.RequestContext) { c.Set("agent_id", int64(42)) }, svc.recordMaintenanceBatch)
	event := map[string]interface{}{"event_id": strings.Repeat("a", 64), "attempt_id": strings.Repeat("b", 32), "event_at": time.Now().UnixMilli(), "component": "dispatch", "trigger": "auto", "phase": "commission", "result": "started", "commission": map[string]interface{}{"prompt": "private content"}}
	status, _, _ := performJSON(t, h, "POST", "/test", map[string]interface{}{"events": []interface{}{event}})
	if status != 400 {
		t.Fatalf("raw prompt accepted: %d", status)
	}
}
