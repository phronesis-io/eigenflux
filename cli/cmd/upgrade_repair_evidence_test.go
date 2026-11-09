package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/maintenance"
)

func TestUpgradeRepairTriggerEvidenceRejectsPreTriggerForegroundAndSameMillisecond(t *testing.T) {
	scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	thread, turn := guardTestThread, "actual-trigger"
	if err := admitHeartbeatHostRun(scope, thread, turn, ""); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.RecordLifecycle(scope, heartbeatHostAttempt(thread, turn), "trigger", "observed", maintenance.RuntimeObservation{Product: "codex"}); err != nil {
		t.Fatal(err)
	}
	at, err := maintenance.LifecycleObservedAt(scope, heartbeatHostAttempt(thread, turn), "trigger", "observed")
	if err != nil || at == 0 {
		t.Fatal("trigger missing", at, err)
	}
	for _, started := range []int64{at - 1, at} {
		c := maintenance.Cycle{ID: strings.Repeat("a", 32), Result: "completed", Mode: "full", StartedAt: started}
		path := upgradeRepairEvidenceTranscript(t, turn, c.ID)
		if got, err := upgradeRepairTriggerAt(scope, thread, turn, c, path); err != nil || got != 0 {
			t.Fatalf("foreground or ambiguous cycle accepted: started=%d trigger=%d result=%d err=%v", started, at, got, err)
		}
	}
}

func TestUpgradeRepairTriggerEvidenceUsesOriginalTriggerAfterContinuation(t *testing.T) {
	scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	thread, original, resumed := guardTestThread, "original-trigger", "resumed-trigger"
	origin := heartbeatHostAttempt(thread, original)
	if err := admitHeartbeatHostRun(scope, thread, original, ""); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.RecordLifecycle(scope, origin, "trigger", "observed", maintenance.RuntimeObservation{Product: "codex"}); err != nil {
		t.Fatal(err)
	}
	at, err := maintenance.LifecycleObservedAt(scope, origin, "trigger", "observed")
	if err != nil || at == 0 {
		t.Fatal("trigger missing", at, err)
	}
	// The next millisecond is required: a same-tick receipt is ambiguous evidence.
	for time.Now().UnixMilli() <= at {
		time.Sleep(time.Millisecond)
	}
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
	if err = admitHeartbeatHostRun(scope, thread, resumed, c.ID, origin); err != nil {
		t.Fatal(err)
	}
	path := upgradeRepairEvidenceTranscript(t, original, c.ID)
	if got, err := upgradeRepairTriggerAt(scope, thread, resumed, c, path); err != nil || got != at {
		t.Fatalf("continuation lost original trigger: got=%d want=%d err=%v", got, at, err)
	}
	wrong := c
	wrong.ID = strings.Repeat("b", 32)
	if got, err := upgradeRepairTriggerAt(scope, thread, resumed, wrong, path); err != nil || got != 0 {
		t.Fatalf("another cycle matched continuation: %d %v", got, err)
	}
}

func upgradeRepairEvidenceTranscript(t *testing.T, turn, id string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	appendHeartbeatHostCommand(t, path, turn, "eigenflux heartbeat plan --format agent", "completed", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle "+id)
	return path
}

func TestUpgradeRepairTriggerEvidenceRejectsUnprovenOriginPlan(t *testing.T) {
	scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	thread, original, resumed := guardTestThread, "origin", "continued"
	origin := heartbeatHostAttempt(thread, original)
	if err := admitHeartbeatHostRun(scope, thread, original, ""); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.RecordLifecycle(scope, origin, "trigger", "observed", maintenance.RuntimeObservation{Product: "codex"}); err != nil {
		t.Fatal(err)
	}
	at, _ := maintenance.LifecycleObservedAt(scope, origin, "trigger", "observed")
	c := maintenance.Cycle{ID: strings.Repeat("a", 32), Result: "completed", Mode: "full", StartedAt: at + 1}
	if err := admitHeartbeatHostRun(scope, thread, resumed, c.ID, origin); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, planTurn, command, output, status string }{
		{"cycle-check-only", original, "eigenflux heartbeat cycle-check", `{"cycle_id":"` + c.ID + `","status":"completed"}`, "completed"},
		{"cycle-check-header", original, "eigenflux heartbeat cycle-check", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle " + c.ID, "completed"},
		{"other-turn", "unrelated-foreground", "eigenflux heartbeat plan", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle " + c.ID, "completed"},
		{"continuation-without-origin", resumed, "eigenflux heartbeat plan", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle " + c.ID, "completed"},
		{"failed-plan", original, "eigenflux heartbeat plan", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle " + c.ID, "failed"},
		{"quoted-plan", original, "eigenflux heartbeat plan", "quoted EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle " + c.ID, "completed"},
		{"truncated-plan", original, "eigenflux heartbeat plan", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle ", "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			appendHeartbeatHostCommand(t, path, tc.planTurn, tc.command, tc.status, tc.output)
			if got, err := upgradeRepairTriggerAt(scope, thread, resumed, c, path); err != nil || got != 0 {
				t.Fatalf("unproven plan established scheduling: %d %v", got, err)
			}
		})
	}
	if got, err := upgradeRepairTriggerAt(scope, thread, resumed, c); err != nil || got != 0 {
		t.Fatal("missing transcript accepted", got, err)
	}
	if got, err := upgradeRepairTriggerAt(scope, thread, resumed, c, filepath.Join(t.TempDir(), "missing")); err == nil || got != 0 {
		t.Fatal("unreadable transcript accepted", got, err)
	}
	if err := admitHeartbeatHostRun(scope, thread, "unknown-origin", c.ID, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	path := upgradeRepairEvidenceTranscript(t, original, c.ID)
	if got, err := upgradeRepairTriggerAt(scope, thread, "unknown-origin", c, path); err != nil || got != 0 {
		t.Fatal("unknown origin guessed a historical plan", got, err)
	}
}

func TestUpgradeRepairPurePlanCycleAcceptsOnlyUnambiguousPlan(t *testing.T) {
	id := strings.Repeat("a", 32)
	prompt := "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle " + id
	body, err := json.Marshal(map[string]string{"schema_version": "eigenflux_heartbeat_plan.v1", "agent_prompt": prompt})
	if err != nil {
		t.Fatal(err)
	}
	if upgradeRepairPlanCycle(prompt) != id || upgradeRepairPlanCycle(string(body)) != id {
		t.Fatal("valid plan not recognized")
	}
	if upgradeRepairPlanCycle(prompt+"\nheartbeat cycle-report --cycle "+strings.Repeat("b", 32)) != "" || upgradeRepairPlanCycle(`{"cycle_id":"`+id+`","status":"completed"}`) != "" {
		t.Fatal("ambiguous plan or check became plan evidence")
	}
}

func TestUpgradeRepairTriggerEvidenceDoesNotInventCanonicalAdmission(t *testing.T) {
	scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c := maintenance.Cycle{ID: strings.Repeat("a", 32), Result: "completed", Mode: "full", StartedAt: time.Now().UnixMilli()}
	if got, err := upgradeRepairTriggerAt(scope, guardTestThread, "foreground", c); err != nil || got != 0 {
		t.Fatal("foreground turn accepted", got, err)
	}
	if err := admitHeartbeatHostRun(scope, guardTestThread, "without-trigger", c.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := upgradeRepairTriggerAt(scope, guardTestThread, "without-trigger", c); err != nil || got != 0 {
		t.Fatal("marker alone established trigger", got, err)
	}
}
