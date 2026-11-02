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
