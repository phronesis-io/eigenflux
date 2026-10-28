package consolev2

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

func commissionActionTestEvent() telemetryEventRequest {
	return telemetryEventRequest{EventID: "commission-action-event-123", EventType: "commission_order_action", EventAt: time.Now().UnixMilli(), Properties: map[string]interface{}{
		"route": "/dashboard/trade", "action": "open_payment", "result": "started", "entity_type": "commission_order", "entity_id": "123",
	}}
}

func TestCommissionOrderActionTelemetryCategories(t *testing.T) {
	for _, action := range []string{"open_payment", "copy_order_instruction"} {
		for _, result := range []string{"started", "succeeded", "failed"} {
			e := commissionActionTestEvent()
			e.Properties["action"], e.Properties["result"] = action, result
			if err := validateTelemetryEvent(e, time.Now().UnixMilli()); err != nil {
				t.Fatalf("%s/%s: %v", action, result, err)
			}
		}
	}
	e := commissionActionTestEvent()
	delete(e.Properties, "entity_id")
	if err := validateTelemetryEvent(e, time.Now().UnixMilli()); err != nil {
		t.Fatalf("optional entity reference: %v", err)
	}
	// Preserve the existing events' property contract.
	e.EventType = "goal_edited"
	e.Properties = map[string]interface{}{"route": "/dashboard/network-goal", "source": "manual"}
	if err := validateTelemetryEvent(e, time.Now().UnixMilli()); err != nil {
		t.Fatalf("existing event changed: %v", err)
	}
}

func TestCommissionOrderActionTelemetryRejectsPrivateAndArbitraryProperties(t *testing.T) {
	for name, mutate := range map[string]func(map[string]interface{}){
		"arbitrary action": func(p map[string]interface{}) { p["action"] = "private content" },
		"nested action":    func(p map[string]interface{}) { p["action"] = map[string]interface{}{"prompt": "private"} },
		"unused action":    func(p map[string]interface{}) { p["action"] = "confirm_delivery" },
		"invented result":  func(p map[string]interface{}) { p["result"] = "paid" },
		"route query":      func(p map[string]interface{}) { p["route"] = "/dashboard/trade?token=secret" },
		"raw body":         func(p map[string]interface{}) { p["body"] = "private content" },
		"source content":   func(p map[string]interface{}) { p["source"] = "private content" },
		"step content":     func(p map[string]interface{}) { p["step"] = "private content" },
		"wrong entity":     func(p map[string]interface{}) { p["entity_type"] = "account" },
		"noncanonical ID":  func(p map[string]interface{}) { p["entity_id"] = "00123" },
		"numeric ID":       func(p map[string]interface{}) { p["entity_id"] = 123 },
		"empty ID":         func(p map[string]interface{}) { p["entity_id"] = "" },
		"missing action":   func(p map[string]interface{}) { delete(p, "action") },
	} {
		t.Run(name, func(t *testing.T) {
			e := commissionActionTestEvent()
			mutate(e.Properties)
			if validateTelemetryEvent(e, time.Now().UnixMilli()) == nil {
				t.Fatal("invalid trade action accepted")
			}
		})
	}
}

func TestCommissionOrderActionHTTPRejectsBeforeStorage(t *testing.T) {
	svc := &Service{telemetryRates: map[string]telemetryRateState{}}
	h := server.New()
	h.POST("/test", func(_ context.Context, c *app.RequestContext) { c.Set("console_session_id", "trade-session-123") }, svc.recordTelemetryBatch)
	e := commissionActionTestEvent()
	e.Properties["action"] = map[string]interface{}{"body": "private content"}
	status, _, _ := performJSON(t, h, "POST", "/test", map[string]interface{}{"events": []telemetryEventRequest{e}})
	if status != 400 {
		t.Fatalf("invalid trade action reached storage: %d", status)
	}
}
