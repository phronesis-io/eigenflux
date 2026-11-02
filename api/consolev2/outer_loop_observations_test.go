package consolev2

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestOuterLoopWireContract(t *testing.T) {
	b, err := os.ReadFile("../../contracts/outer_loop_observations.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name  string
		Valid bool
		Event json.RawMessage
	}
	if err = json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			var event maintenanceEvent
			d := json.NewDecoder(bytes.NewReader(tc.Event))
			d.DisallowUnknownFields()
			err := d.Decode(&event)
			if err == nil {
				err = validateMaintenanceEvent(event, time.UnixMilli(event.EventAt))
			}
			if (err == nil) != tc.Valid {
				t.Fatalf("valid=%t err=%v", tc.Valid, err)
			}
		})
	}
}

func TestSchedulerReadbackCapabilityRequiresNewCLIAndExistingIdentity(t *testing.T) {
	registry := buildAgentCapabilityRegistry("en", true, true)
	for _, op := range registry["operations"].([]capabilityOperation) {
		if op.OperationID == "heartbeat.scheduler_report" {
			if op.MinCLIVersion != "1.0.3" || op.IdentityRoute != "current_identity" || op.Confirmation != "policy_governed" {
				t.Fatalf("incorrect scheduler readback boundary: %+v", op)
			}
			return
		}
	}
	t.Fatal("scheduler readback capability missing")
}
