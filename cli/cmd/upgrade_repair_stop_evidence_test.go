package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/maintenance"
)

func finishRepairEvidenceCycle(t *testing.T, scope maintenance.Scope) maintenance.Cycle {
	t.Helper()
	c, err := maintenance.StartCycle(scope, "full", []string{"feed"}, maintenance.RuntimeObservation{Product: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if err = maintenance.ReportCycle(scope, c.ID, "feed", "completed"); err != nil {
		t.Fatal(err)
	}
	if err = maintenance.ReportCycle(scope, c.ID, "cycle", "completed"); err != nil {
		t.Fatal(err)
	}
	c, err = maintenance.ReadCycle(scope, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUpgradeRepairStopRejectsForegroundCycleReadbackThenAcceptsActualScheduledCycle(t *testing.T) {
	_, server := runtimeTestConfig(t, "https://repair.example.invalid", true)
	r, err := currentUpgradeRepair(true)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := maintenanceScope(server)
	if err != nil {
		t.Fatal(err)
	}
	definition := strings.Repeat("d", 64)
	if err = bindUpgradeRepairScheduler(scope, guardTestThread, definition); err != nil {
		t.Fatal(err)
	}
	foreground := finishRepairEvidenceCycle(t, scope)
	// This period was completed before the scheduled turn was admitted.
	for time.Now().UnixMilli() <= foreground.StartedAt {
		time.Sleep(time.Millisecond)
	}
	in := heartbeatHostInput{heartbeatStopInput: heartbeatStopInput{SessionID: guardTestThread, TurnID: "later-scheduled-turn", Event: "UserPromptSubmit"}, Prompt: "Run one EigenFlux heartbeat cycle. Execute directly: exact scoped launcher."}
	in.ExpectedPromptHash = heartbeatTestPromptHash(in.Prompt)
	if err = observeHeartbeatHost(scope, in, guardTestThread); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err = os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	appendHeartbeatHostCommand(t, path, in.TurnID, "eigenflux heartbeat cycle-check --cycle "+foreground.ID+" --finish", "completed", `{"cycle_id":"`+foreground.ID+`","status":"completed"}`)
	in.Event, in.Transcript, in.GuardDefinition = "Stop", path, definition
	if _, err = heartbeatStopDecision(scope, in.heartbeatStopInput, guardTestThread); err != nil {
		t.Fatal(err)
	}
	state, err := currentUpgradeRepair(false)
	if err != nil || state.Status == "verified" || state.ID != r.ID {
		t.Fatalf("foreground readback falsely verified scheduling: %+v %v", state, err)
	}
	at, err := maintenance.LifecycleObservedAt(scope, heartbeatHostAttempt(guardTestThread, in.TurnID), "trigger", "observed")
	if err != nil || at == 0 {
		t.Fatal("canonical trigger not recorded", at, err)
	}
	for time.Now().UnixMilli() <= at {
		time.Sleep(time.Millisecond)
	}
	concurrentForeground := finishRepairEvidenceCycle(t, scope)
	appendHeartbeatHostCommand(t, path, in.TurnID, "eigenflux heartbeat cycle-check --cycle "+concurrentForeground.ID+" --finish", "completed", `{"cycle_id":"`+concurrentForeground.ID+`","status":"completed"}`)
	if _, err = heartbeatStopDecision(scope, in.heartbeatStopInput, guardTestThread); err != nil {
		t.Fatal(err)
	}
	state, err = currentUpgradeRepair(false)
	if err != nil || state.Status == "verified" {
		t.Fatalf("concurrent foreground cycle after trigger falsely verified scheduling: %+v %v", state, err)
	}
	scheduled, err := maintenance.StartCycle(scope, "full", []string{"feed"}, maintenance.RuntimeObservation{Product: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	appendHeartbeatHostCommand(t, path, in.TurnID, "eigenflux heartbeat plan --format agent", "completed", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle "+scheduled.ID)
	if err = maintenance.ReportCycle(scope, scheduled.ID, "feed", "completed"); err != nil {
		t.Fatal(err)
	}
	if err = maintenance.ReportCycle(scope, scheduled.ID, "cycle", "completed"); err != nil {
		t.Fatal(err)
	}
	appendHeartbeatHostCommand(t, path, in.TurnID, "eigenflux heartbeat cycle-check --cycle "+scheduled.ID+" --finish", "completed", `{"cycle_id":"`+scheduled.ID+`","status":"completed"}`)
	if _, err = heartbeatStopDecision(scope, in.heartbeatStopInput, guardTestThread); err != nil {
		t.Fatal(err)
	}
	state, err = currentUpgradeRepair(false)
	if err != nil || state.Status != "verified" {
		t.Fatalf("actual post-trigger completed cycle did not verify: %+v %v", state, err)
	}
}

func TestUpgradeRepairStopRejectsTriggerFromBeforeAcceptanceRequest(t *testing.T) {
	_, server := runtimeTestConfig(t, "https://repair.example.invalid", true)
	scope, err := maintenanceScope(server)
	if err != nil {
		t.Fatal(err)
	}
	in := heartbeatHostInput{heartbeatStopInput: heartbeatStopInput{SessionID: guardTestThread, TurnID: "already-running-turn", Event: "UserPromptSubmit"}, Prompt: "Run one EigenFlux heartbeat cycle. Execute directly: exact scoped launcher."}
	in.ExpectedPromptHash = heartbeatTestPromptHash(in.Prompt)
	if err = observeHeartbeatHost(scope, in, guardTestThread); err != nil {
		t.Fatal(err)
	}
	at, err := maintenance.LifecycleObservedAt(scope, heartbeatHostAttempt(guardTestThread, in.TurnID), "trigger", "observed")
	if err != nil || at == 0 {
		t.Fatal("trigger missing", at, err)
	}
	for time.Now().UnixMilli() <= at {
		time.Sleep(time.Millisecond)
	}
	if _, err = currentUpgradeRepair(true); err != nil {
		t.Fatal(err)
	}
	definition := strings.Repeat("e", 64)
	if err = bindUpgradeRepairScheduler(scope, guardTestThread, definition); err != nil {
		t.Fatal(err)
	}
	c := finishRepairEvidenceCycle(t, scope)
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err = os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	appendHeartbeatHostCommand(t, path, in.TurnID, "eigenflux heartbeat cycle-check --cycle "+c.ID+" --finish", "completed", `{"cycle_id":"`+c.ID+`","status":"completed"}`)
	appendHeartbeatHostCommand(t, path, in.TurnID, "eigenflux heartbeat plan --format agent", "completed", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle "+c.ID)
	in.Event, in.Transcript, in.GuardDefinition = "Stop", path, definition
	if _, err = heartbeatStopDecision(scope, in.heartbeatStopInput, guardTestThread); err != nil {
		t.Fatal(err)
	}
	state, err := currentUpgradeRepair(false)
	if err != nil || state.Status == "verified" {
		t.Fatalf("pre-request trigger established new acceptance: %+v %v", state, err)
	}
}
