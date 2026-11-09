package maintenance

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestCycleObservationsSurviveFlushAndLegacyStateWrites(t *testing.T) {
	scope := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	cycle, err := StartCycle(scope, "full", []string{"feed"}, RuntimeObservation{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ObserveCycle(scope, cycle.ID, "plan_created"); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := Snapshot(scope)
	var stale Event
	for _, event := range snapshot.Events {
		if event.Result == "plan_created" {
			stale = event
		}
	}
	if stale.EventID == "" {
		t.Fatal("missing plan observation")
	}
	sent := 0
	send := func(events []Event) error {
		for _, e := range events {
			if e.EventID == stale.EventID {
				sent++
			}
		}
		return nil
	}
	if err := Flush(scope, time.Now(), send); err != nil {
		t.Fatal(err)
	}
	path, _ := scope.path()
	body, _ := json.Marshal(snapshot)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ObserveCycle(scope, cycle.ID, "plan_created"); err != nil {
		t.Fatal(err)
	}
	if err := Flush(scope, time.Now().Add(time.Minute), send); err != nil {
		t.Fatal(err)
	}
	if sent != 1 {
		t.Fatalf("acknowledged plan resent %d times", sent)
	}
	current, _ := Snapshot(scope)
	c := current.Cycles[cycle.ID]
	if c.Result != "" || c.ExecutionObserved || c.Stages["feed"] != "" {
		t.Fatal("observation claimed execution or terminal outcome")
	}
	if err := ObserveCycle(scope, cycle.ID, "incomplete"); err != nil {
		t.Fatal(err)
	}
	if err := ReportCycle(scope, cycle.ID, "feed", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := ReportCycle(scope, cycle.ID, "cycle", "completed"); err != nil {
		t.Fatal(err)
	}
}

func TestCycleObservationRestoresPendingQueueAfterLegacyWrite(t *testing.T) {
	scope := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	cycle, _ := StartCycle(scope, "full", []string{"feed"}, RuntimeObservation{})
	before, _ := Snapshot(scope)
	if err := ObserveCycle(scope, cycle.ID, "permission_required"); err != nil {
		t.Fatal(err)
	}
	path, _ := scope.path()
	body, _ := json.Marshal(before)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	found := 0
	if err := Flush(scope, time.Now(), func(events []Event) error {
		for _, e := range events {
			if e.Result == "permission_required" {
				found++
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if found != 1 {
		t.Fatal("pending sidecar observation lost")
	}
	other := scope
	other.AgentID = "2"
	if ObserveCycle(other, cycle.ID, "incomplete") == nil {
		t.Fatal("accepted another account cycle")
	}
	if ObserveCycle(scope, cycle.ID, "failed") == nil {
		t.Fatal("observation accepted terminal result")
	}
}

func TestRuntimeLifecycleAndCycleObservationContract(t *testing.T) {
	scope := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	valid := map[string][]string{"trigger": {"observed"}, "permission": {"allowed", "legacy_mismatch", "prompt", "forbidden", "unknown", "required"}, "guard": {"configured", "active"}, "execution": {"interrupted"}}
	for stage, results := range valid {
		for _, result := range results {
			attempt := NewID()
			if err := RecordLifecycle(scope, attempt, stage, result, RuntimeObservation{Product: "codex", CLI: "1.0.19", PlanMode: "full"}); err != nil {
				t.Fatalf("%s/%s: %v", stage, result, err)
			}
			if err := RecordLifecycle(scope, attempt, stage, result, RuntimeObservation{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	st, _ := Snapshot(scope)
	if len(st.Events) != 10 {
		t.Fatalf("duplicate lifecycle observations: %d", len(st.Events))
	}
	for _, e := range st.Events {
		if e.Runtime.PlanMode != "" {
			t.Fatal("lifecycle claimed plan mode")
		}
	}
	for _, stage := range []string{"feed", "cycle", "private text"} {
		if RecordLifecycle(scope, NewID(), stage, "observed", RuntimeObservation{}) == nil {
			t.Fatal("invalid lifecycle stage")
		}
	}
	if RecordLifecycle(scope, NewID(), "permission", "allowed", RuntimeObservation{Model: "raw private text"}) == nil {
		t.Fatal("free text accepted")
	}
	e := NewEvent(NewID(), "runtime", "auto", "cycle", "incomplete")
	e.Runtime = &RuntimeObservation{Stage: "feed", PlanMode: "full"}
	if Validate(e) == nil {
		t.Fatal("diagnostic counted as business-stage receipt")
	}
	e.Runtime.Stage = "cycle"
	for _, result := range []string{"plan_created", "incomplete", "permission_required", "interrupted"} {
		e.Result = result
		if err := Validate(e); err != nil {
			t.Fatal(err)
		}
	}
	e.Phase = "lifecycle"
	e.Result = "observed"
	e.Runtime.Stage = "trigger"
	if Validate(e) == nil {
		t.Fatal("lifecycle accepted plan mode")
	}
}

func TestCorruptObservationSidecarDoesNotBlockLegacyMaintenance(t *testing.T) {
	scope := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	event := NewEvent(NewID(), "notifier", "auto", "notification", "ready")
	event.Platform, event.Source = "darwin", "watch"
	event.Notification = &Notification{Action: "status", Visibility: "unknown"}
	if err := Record(scope, event); err != nil {
		t.Fatal(err)
	}
	if err := writeGateJSON(scope, "observations", map[string]string{"broken": "receipt"}); err != nil {
		t.Fatal(err)
	}
	sent := 0
	if err := Flush(scope, time.Now(), func(events []Event) error { sent += len(events); return nil }); err != nil {
		t.Fatal(err)
	}
	if sent != 1 {
		t.Fatal("new telemetry blocked legacy upload")
	}
	snapshot, _ := Snapshot(scope)
	if len(snapshot.Events) != 0 {
		t.Fatal("legacy acknowledgement lost")
	}
}

func TestCorruptObservationSidecarDoesNotBlockUninstallOrRuntime(t *testing.T) {
	scope := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	uninstall := NewEvent(NewID(), "cli", "manual", "uninstall", "removed")
	uninstall.Source, uninstall.Platform, uninstall.ReasonCategory = "agent", "darwin", "not_provided"
	if err := Record(scope, uninstall); err != nil {
		t.Fatal(err)
	}
	cycle, _ := StartCycle(scope, "full", []string{"feed"}, RuntimeObservation{})
	if err := ObserveCycle(scope, cycle.ID, "plan_created"); err != nil {
		t.Fatal(err)
	}
	if err := writeGateJSON(scope, "observations", map[string]string{"future": "schema"}); err != nil {
		t.Fatal(err)
	}
	sent := map[string]int{}
	send := func(events []Event) error {
		for _, event := range events {
			sent[event.EventID]++
		}
		return nil
	}
	if err := FlushUninstall(scope, time.Now(), uninstall.AttemptID, send); err != nil {
		t.Fatal(err)
	}
	if sent[uninstall.EventID] != 1 {
		t.Fatal("corrupt diagnostics blocked uninstall")
	}
	if err := Flush(scope, time.Now(), send); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := Snapshot(scope)
	if len(snapshot.Events) != 0 {
		t.Fatal("corrupt diagnostics blocked queued runtime acknowledgement")
	}
	path, _ := gatePath(scope, "observations")
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "{\"future\":\"schema\"}" {
		t.Fatal("corrupt sidecar erased")
	}
}
