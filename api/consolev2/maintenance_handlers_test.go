package consolev2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

func validMaintenanceEvent() maintenanceEvent {
	return maintenanceEvent{EventID: strings.Repeat("a", 64), AttemptID: strings.Repeat("b", 32), EventAt: time.Now().UnixMilli(), Component: "cli", Trigger: "auto", Phase: "execute", Result: "executed", ToVersion: "1.0.0", RunningVersion: "1.0.0"}
}
func TestMaintenanceEventContractRejectsUnsafeOrImpossibleClaims(t *testing.T) {
	now := time.Now()
	good := validMaintenanceEvent()
	if err := validateMaintenanceEvent(good, now); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*maintenanceEvent)
	}{
		{"old", func(e *maintenanceEvent) { e.EventAt = now.Add(-8 * 24 * time.Hour).UnixMilli() }},
		{"future", func(e *maintenanceEvent) { e.EventAt = now.Add(time.Hour).UnixMilli() }},
		{"wrong component", func(e *maintenanceEvent) { e.Component = "skills" }},
		{"unknown mode", func(e *maintenanceEvent) { e.Mode = "invented" }},
		{"raw path", func(e *maintenanceEvent) { e.ErrorCode = "/Users/private/home" }},
		{"message", func(e *maintenanceEvent) { e.ErrorCode = "token expired for owner@example.com" }},
		{"invented result", func(e *maintenanceEvent) { e.Result = "success" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := good
			test.mutate(&e)
			if validateMaintenanceEvent(e, now) == nil {
				t.Fatal("invalid event accepted")
			}
		})
	}
}
func TestMaintenanceHTTPRejectsUnauthenticatedAndIdentityInput(t *testing.T) {
	svc := &Service{telemetryRates: map[string]telemetryRateState{}}
	h := server.New()
	h.POST("/missing", svc.recordMaintenanceBatch)
	status, _, _ := performJSON(t, h, "POST", "/missing", map[string]interface{}{"events": []maintenanceEvent{validMaintenanceEvent()}})
	if status != 401 {
		t.Fatalf("unauthenticated request status=%d", status)
	}
	h.POST("/test", func(_ context.Context, c *app.RequestContext) { c.Set("agent_id", int64(42)) }, svc.recordMaintenanceBatch)
	status, _, _ = performJSON(t, h, "POST", "/test", map[string]interface{}{"agent_id": "99", "events": []maintenanceEvent{validMaintenanceEvent()}})
	if status != 400 {
		t.Fatalf("client-supplied identity accepted: %d", status)
	}
	event := map[string]interface{}{"event_id": strings.Repeat("a", 64), "attempt_id": strings.Repeat("b", 32), "event_at": time.Now().UnixMilli(), "component": "cli", "trigger": "auto", "phase": "execute", "result": "executed", "home": "/private/home"}
	status, _, _ = performJSON(t, h, "POST", "/test", map[string]interface{}{"events": []interface{}{event}})
	if status != 400 {
		t.Fatalf("private unknown event property accepted: %d", status)
	}
}
