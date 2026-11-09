package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/maintenance"
	watchstate "cli.eigenflux.ai/internal/watch"
)

func repairContinuationFixture(t *testing.T) (maintenance.Scope, maintenance.Cycle, heartbeatHostInput, string) {
	t.Helper()
	_, server := runtimeTestConfig(t, "https://repair.example.invalid", true)
	if _, err := currentUpgradeRepair(true); err != nil {
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
	r, _ := currentUpgradeRepair(false)
	if _, err = reportUpgradeRepair(r.ID, "awaiting_run", nil); err != nil {
		t.Fatal(err)
	}
	in := heartbeatHostInput{heartbeatStopInput: heartbeatStopInput{SessionID: guardTestThread, TurnID: "original-scheduled", Event: "UserPromptSubmit", GuardDefinition: definition}, Prompt: "Run one EigenFlux heartbeat cycle. Execute directly: exact scoped launcher."}
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
	c, err := maintenance.StartCycle(scope, "full", []string{"feed"}, maintenance.RuntimeObservation{Product: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	path := upgradeRepairEvidenceTranscript(t, in.TurnID, c.ID)
	in.Event, in.Transcript = "Stop", path
	return scope, c, in, path
}

func TestUpgradeRepairActualStopContinuationPreservesCanonicalOrigin(t *testing.T) {
	for _, truncateOriginal := range []bool{false, true} {
		name := "full-rollout"
		if truncateOriginal {
			name = "original-plan-truncated-after-proven-stop"
		}
		t.Run(name, func(t *testing.T) {
			scope, c, in, path := repairContinuationFixture(t)
			origin := heartbeatHostAttempt(guardTestThread, in.TurnID)
			decision, err := heartbeatStopDecision(scope, in.heartbeatStopInput, guardTestThread)
			if err != nil || decision["decision"] != "block" {
				t.Fatalf("unfinished real cycle did not continue: %v %v", decision, err)
			}
			index, ok := readHeartbeatHostCycleOrigin(scope, guardTestThread, c.ID)
			if !ok || index.OriginAttempt != origin {
				t.Fatalf("first real Stop did not pin origin: %+v %v", index, ok)
			}
			in.Event, in.TurnID, in.Prompt = "UserPromptSubmit", "automatic-continuation", decision["reason"].(string)
			if err = observeHeartbeatHost(scope, in, guardTestThread); err != nil {
				t.Fatal(err)
			}
			marker, ok := readHeartbeatHostMarker(scope, guardTestThread, heartbeatHostAttempt(guardTestThread, in.TurnID))
			if !ok || marker.Cycle != c.ID || marker.OriginAttempt != origin {
				t.Fatalf("real continuation invented a new origin: %+v %v", marker, ok)
			}
			if observed, err := maintenance.LifecycleObserved(scope, heartbeatHostAttempt(guardTestThread, in.TurnID), "trigger", "observed"); err != nil || observed {
				t.Fatal("continuation invented a scheduler trigger", observed, err)
			}
			if truncateOriginal {
				path = filepath.Join(t.TempDir(), "continued-rollout.jsonl")
				if err = os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err = maintenance.ReportCycle(scope, c.ID, "feed", "completed"); err != nil {
				t.Fatal(err)
			}
			if err = maintenance.ReportCycle(scope, c.ID, "cycle", "completed"); err != nil {
				t.Fatal(err)
			}
			appendHeartbeatHostCommand(t, path, in.TurnID, "eigenflux heartbeat cycle-check --cycle "+c.ID+" --finish", "completed", `{"cycle_id":"`+c.ID+`","status":"completed"}`)
			in.Event, in.Transcript, in.StopHookActive = "Stop", path, true
			if decision, err = heartbeatStopDecision(scope, in.heartbeatStopInput, guardTestThread); err != nil || len(decision) != 0 {
				t.Fatalf("completed continuation could not finish: %v %v", decision, err)
			}
			state, err := currentUpgradeRepair(false)
			if err != nil || state.Status != "verified" {
				t.Fatalf("real completed continuation did not verify: %+v %v", state, err)
			}
		})
	}
}

func TestUpgradeRepairCycleOriginIsScopedStableAndRejectsUnknownEvidence(t *testing.T) {
	scope, c, in, _ := repairContinuationFixture(t)
	if err := bindHeartbeatHostCycle(scope, in.heartbeatStopInput, c); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(heartbeatHostRunPath(scope, guardTestThread, "")), heartbeatHostCycleOriginKey(c.ID)+".json")
	before, err := os.Stat(path)
	if err != nil || before.Size() > 256 {
		t.Fatal("unbounded origin receipt", before, err)
	}
	if err = bindHeartbeatHostCycle(scope, in.heartbeatStopInput, c); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("idempotent origin binding refreshed evidence age")
	}
	if _, ok := readHeartbeatHostCycleOrigin(scope, "different-owner-thread", c.ID); ok {
		t.Fatal("origin crossed owner thread")
	}
	other := scope
	other.AgentID = "other-agent"
	if _, ok := readHeartbeatHostCycleOrigin(other, guardTestThread, c.ID); ok {
		t.Fatal("origin crossed account")
	}
	foreign := finishRepairEvidenceCycle(t, scope)
	in.Transcript = upgradeRepairEvidenceTranscript(t, "different-foreground-turn", foreign.ID)
	if err := bindHeartbeatHostCycle(scope, in.heartbeatStopInput, foreign); err != nil {
		t.Fatal(err)
	}
	if _, ok := readHeartbeatHostCycleOrigin(scope, guardTestThread, foreign.ID); ok {
		t.Fatal("unrelated foreground plan became canonical origin")
	}
}

func TestUpgradeRepairDamagedCycleOriginDoesNotChangeBusinessOutcome(t *testing.T) {
	scope, c, in, path := repairContinuationFixture(t)
	if _, err := heartbeatStopDecision(scope, in.heartbeatStopInput, guardTestThread); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(filepath.Dir(heartbeatHostRunPath(scope, guardTestThread, "")), heartbeatHostCycleOriginKey(c.ID)+".json")
	damaged := []byte(`{"cycle_id":"unusable"}`)
	if err := os.WriteFile(indexPath, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.ReportCycle(scope, c.ID, "feed", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.ReportCycle(scope, c.ID, "cycle", "completed"); err != nil {
		t.Fatal(err)
	}
	appendHeartbeatHostCommand(t, path, in.TurnID, "eigenflux heartbeat cycle-check --cycle "+c.ID+" --finish", "completed", `{"cycle_id":"`+c.ID+`","status":"completed"}`)
	if decision, err := heartbeatStopDecision(scope, in.heartbeatStopInput, guardTestThread); err != nil || len(decision) != 0 {
		t.Fatal("optional origin persistence changed completed business outcome", decision, err)
	}
	actual, _ := os.ReadFile(indexPath)
	if string(actual) != string(damaged) {
		t.Fatal("damaged origin evidence was overwritten")
	}
	cycle, err := maintenance.ReadCycle(scope, c.ID)
	if err != nil || cycle.Result != "completed" {
		t.Fatal("business completion changed", cycle, err)
	}
	state, err := currentUpgradeRepair(false)
	if err != nil || state.Status == "verified" {
		t.Fatal("damaged origin claimed acceptance", state, err)
	}
}

func TestUpgradeRepairOriginPruningPreservesHeldLockPath(t *testing.T) {
	scope, _, _, _ := repairContinuationFixture(t)
	dir := filepath.Dir(heartbeatHostRunPath(scope, guardTestThread, ""))
	lockPath := filepath.Join(dir, ".cycle-origins.lock")
	unlock, err := watchstate.AcquirePath(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	expired := time.Now().Add(-maintenance.Retention - time.Hour)
	if err = os.Chtimes(lockPath, expired, expired); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(dir, strings.Repeat("a", 64)+".json")
	if err = os.WriteFile(markerPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(markerPath, expired, expired); err != nil {
		t.Fatal(err)
	}
	if err = admitHeartbeatHostRun(scope, guardTestThread, "later-admission", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(lockPath); err != nil {
		t.Fatal("origin lock pathname was removed during pruning", err)
	}
	if _, err = os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatal("expired owned marker was not pruned", err)
	}
}
