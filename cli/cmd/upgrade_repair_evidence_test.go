package cmd

import (
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
		if got, err := upgradeRepairTriggerAt(scope, thread, turn, c); err != nil || got != 0 {
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
	if got, err := upgradeRepairTriggerAt(scope, thread, resumed, c); err != nil || got != at {
		t.Fatalf("continuation lost original trigger: got=%d want=%d err=%v", got, at, err)
	}
	wrong := c
	wrong.ID = strings.Repeat("b", 32)
	if got, err := upgradeRepairTriggerAt(scope, thread, resumed, wrong); err != nil || got != 0 {
		t.Fatalf("another cycle matched continuation: %d %v", got, err)
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
