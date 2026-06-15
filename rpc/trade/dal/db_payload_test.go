package dal

import (
	"encoding/json"
	"testing"
)

func TestMarshalOrderEventPayload(t *testing.T) {
	got, err := MarshalOrderEventPayload(100, 200, 300, "released")
	if err != nil {
		t.Fatalf("MarshalOrderEventPayload: %v", err)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed["outbox_id"] != "100" {
		t.Errorf("outbox_id: want 100, got %q", parsed["outbox_id"])
	}
	if parsed["order_id"] != "200" {
		t.Errorf("order_id: want 200, got %q", parsed["order_id"])
	}
	if parsed["service_id"] != "300" {
		t.Errorf("service_id: want 300, got %q", parsed["service_id"])
	}
	if parsed["event_type"] != "released" {
		t.Errorf("event_type: want released, got %q", parsed["event_type"])
	}
}
