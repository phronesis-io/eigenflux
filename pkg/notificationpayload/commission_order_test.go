package notificationpayload

import (
	"encoding/json"
	"testing"
)

func TestNormalizeCommissionOrderIDsPreservesSnowflakePrecision(t *testing.T) {
	raw := `{"event_id":9223372036854775807,"order_id":9007199254740993,"recipient_agent_id":9007199254740995,"actor_agent_id":0,"snapshot_id":9007199254740997,"order_version":12,"occurred_at":1700000000000}`
	normalized, err := NormalizeCommissionOrderIDs(raw)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(normalized, &payload); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{
		"event_id": "9223372036854775807", "order_id": "9007199254740993",
		"recipient_agent_id": "9007199254740995", "actor_agent_id": "0",
		"snapshot_id": "9007199254740997",
	} {
		if got, ok := payload[field].(string); !ok || got != want {
			t.Fatalf("%s=%#v, want string %q", field, payload[field], want)
		}
	}
	if payload["order_version"] != float64(12) || payload["occurred_at"] != float64(1700000000000) {
		t.Fatalf("non-ID numeric fields changed: %#v", payload)
	}
}

func TestNormalizeCommissionOrderIDsRejectsNonIntegralID(t *testing.T) {
	for _, raw := range []string{
		`{"order_id":1.5}`,
		`{"order_id":{}}`,
		`[]`,
		`{"order_id":1} trailing`,
	} {
		if _, err := NormalizeCommissionOrderIDs(raw); err == nil {
			t.Fatalf("accepted invalid payload %q", raw)
		}
	}
}
