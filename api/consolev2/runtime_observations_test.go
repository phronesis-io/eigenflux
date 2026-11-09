package consolev2

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestRuntimeObservationContract(t *testing.T) {
	e := validMaintenanceEvent()
	e.Component = "runtime"
	e.Phase = "environment"
	e.Result = "observed"
	e.ToVersion = ""
	e.FromVersion = ""
	e.RunningVersion = ""
	e.DurationMS = 0
	e.ErrorCode = ""
	e.Runtime = &runtimeObservation{Product: "codex", OS: "darwin", Arch: "arm64", Model: "provider/model"}
	if err := validateMaintenanceEvent(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.OuterLoop = &outerLoopObservation{}
	if validateMaintenanceEvent(e, time.Now()) == nil {
		t.Fatal("mixed setup and runtime observation accepted")
	}
	e.OuterLoop = nil
	e.Phase = "cycle"
	e.Runtime.Stage = "cycle"
	e.Runtime.PlanMode = "full"
	e.Result = "completed"
	if err := validateMaintenanceEvent(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.Runtime.Stage = ""
	if validateMaintenanceEvent(e, time.Now()) == nil {
		t.Fatal("missing stage")
	}
	e.Runtime.Stage = "cycle"
	e.Runtime.Model = "raw private text"
	if validateMaintenanceEvent(e, time.Now()) == nil {
		t.Fatal("free text")
	}
	dec := json.NewDecoder(bytes.NewBufferString(`{"runtime":{"device_name":"private"}}`))
	dec.DisallowUnknownFields()
	if dec.Decode(&maintenanceEvent{}) == nil {
		t.Fatal("unknown nested field")
	}
}

func TestRuntimeLifecycleAndNonterminalCycleContract(t *testing.T) {
	e := validMaintenanceEvent()
	e.Component, e.Phase, e.Result = "runtime", "cycle", "plan_created"
	e.ToVersion, e.FromVersion, e.RunningVersion, e.ErrorCode = "", "", "", ""
	e.DurationMS = 0
	e.Runtime = &runtimeObservation{Stage: "cycle", PlanMode: "full", Product: "codex", CLI: "1.0.19"}
	for _, result := range []string{"plan_created", "incomplete", "permission_required", "interrupted"} {
		e.Result = result
		if err := validateMaintenanceEvent(e, time.Now()); err != nil {
			t.Fatalf("%s: %v", result, err)
		}
		e.Runtime.Stage = "feed"
		if validateMaintenanceEvent(e, time.Now()) == nil {
			t.Fatal("diagnostic accepted as business stage")
		}
		e.Runtime.Stage = "cycle"
	}
	valid := map[string][]string{"trigger": {"observed"}, "permission": {"allowed", "legacy_mismatch", "prompt", "forbidden", "unknown", "required"}, "guard": {"configured", "active"}, "execution": {"interrupted"}}
	e.Phase = "lifecycle"
	e.Runtime.PlanMode = ""
	for stage, results := range valid {
		for _, result := range results {
			e.Runtime.Stage, e.Result = stage, result
			if err := validateMaintenanceEvent(e, time.Now()); err != nil {
				t.Fatalf("%s/%s: %v", stage, result, err)
			}
		}
	}
	e.Runtime.Stage = "permission"
	e.Result = "required"
	e.ErrorCode = "permission_denied"
	if validateMaintenanceEvent(e, time.Now()) == nil {
		t.Fatal("unexpected raw error field")
	}
	e.ErrorCode = ""
	e.Runtime.PlanMode = "full"
	if validateMaintenanceEvent(e, time.Now()) == nil {
		t.Fatal("lifecycle accepted plan mode")
	}
	e.Runtime.PlanMode = ""
	e.Runtime.Stage = "feed"
	if validateMaintenanceEvent(e, time.Now()) == nil {
		t.Fatal("unexpected lifecycle stage")
	}
}
