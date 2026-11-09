package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/maintenance"
)

func TestUpgradeRepairSurvivesRestartAndGroupsActions(t *testing.T) {
	runtimeTestConfig(t, "https://repair.example.invalid", true)
	r, err := currentUpgradeRepair(true)
	if err != nil {
		t.Fatal(err)
	}
	again, err := currentUpgradeRepair(true)
	if err != nil || again.ID != r.ID {
		t.Fatalf("request replaced: %+v %v", again, err)
	}
	r, err = reportUpgradeRepair(r.ID, "user_action", []string{"host_restart", "hook_review", "hook_review"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Blockers) != 2 || r.Notification != "required" {
		t.Fatalf("actions not combined: %+v", r)
	}
	first := r.NoticeID
	again, err = reportUpgradeRepair(r.ID, "user_action", []string{"hook_review", "host_restart"})
	if err != nil || again.NoticeID != first {
		t.Fatal("ordering changed notice")
	}
	if err := acknowledgeUpgradeRepair(r.ID, first); err != nil {
		t.Fatal(err)
	}
	again, _ = currentUpgradeRepair(false)
	if again.Notification != "unchanged" {
		t.Fatal("restart repeats confirmed prompt")
	}
	if _, err = reportUpgradeRepair(r.ID, "verified", nil); err == nil {
		t.Fatal("agent asserted host acceptance")
	}
	if _, err = reportUpgradeRepair(r.ID, "awaiting_run", []string{"hook_review"}); err == nil {
		t.Fatal("pending action claimed ready")
	}
	if _, err = reportUpgradeRepair(r.ID, "blocked", []string{"untrusted raw error"}); err == nil {
		t.Fatal("accepted unbounded blocker")
	}
	scope, _ := maintenanceScope(activeServerName())
	if err := bindUpgradeRepairScheduler(scope, guardTestThread, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = reportUpgradeRepair(r.ID, "awaiting_run", nil); err != nil {
		t.Fatal(err)
	}
	r, err = reportUpgradeRepair(r.ID, "user_action", []string{"hook_review", "host_restart"})
	if err != nil || r.NoticeID == first || r.Notification != "required" {
		t.Fatalf("regression suppressed %+v %v", r, err)
	}
	if err = acknowledgeUpgradeRepair(r.ID, first); err == nil {
		t.Fatal("stale ack consumed new blocker")
	}
}

func TestUpgradeRepairAccountSwitchCannotConsumeOldReceipt(t *testing.T) {
	_, server := runtimeTestConfig(t, "https://repair.example.invalid", true)
	r, err := currentUpgradeRepair(true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := auth.LoadV2Credentials(server)
	if err != nil {
		t.Fatal(err)
	}
	c.AgentID = "agent-2"
	if err = auth.SaveV2Credentials(server, c); err != nil {
		t.Fatal(err)
	}
	if _, err = reportUpgradeRepair(r.ID, "awaiting_run", nil); err == nil {
		t.Fatal("old account accepted")
	}
	next, err := currentUpgradeRepair(true)
	if err != nil || next.ID == r.ID {
		t.Fatalf("receipt crossed identity: %+v %v", next, err)
	}
}

func TestUpgradeRepairDispatchNeverReplaysUncertainAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runErr error
		want   string
	}{
		{"busy", dispatch.ErrMainNoticeBusy, "pending"},
		{"unknown", errors.New("receipt unavailable"), "blocked"},
		{"success_without_report", nil, "blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeTestConfig(t, "https://repair.example.invalid", true)
			r, _ := currentUpgradeRepair(true)
			_, err := mutateUpgradeRepair(false, func(r *upgradeRepair) error { r.Status = "submitted"; r.Attempts = 1; return nil })
			if err != nil {
				t.Fatal(err)
			}
			out, err := finishUpgradeRepairDispatch(r.ID, tc.runErr)
			if err != nil || out.Status != tc.want {
				t.Fatalf("unsafe retry %+v %v", out, err)
			}
			if out.Status == "pending" && out.NextAttempt <= time.Now().UnixMilli() {
				t.Fatal("no backoff")
			}
		})
	}
}

func TestUpgradeRepairDispatchPreservesAgentAndHookReceipts(t *testing.T) {
	runtimeTestConfig(t, "https://repair.example.invalid", true)
	r, _ := currentUpgradeRepair(true)
	if _, err := reportUpgradeRepair(r.ID, "user_action", []string{"hook_review"}); err != nil {
		t.Fatal(err)
	}
	out, err := finishUpgradeRepairDispatch(r.ID, errors.New("transport ended after report"))
	if err != nil || out.Status != "user_action" {
		t.Fatalf("lost repair receipt %+v %v", out, err)
	}
}

func TestUpgradeRepairRequiresRealScheduledCompletedFullCycle(t *testing.T) {
	_, server := runtimeTestConfig(t, "https://repair.example.invalid", true)
	r, err := currentUpgradeRepair(true)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := maintenanceScope(server)
	if err != nil {
		t.Fatal(err)
	}
	thread, turn := guardTestThread, "actual-turn"
	bindUpgradeRepairScheduler(scope, thread, strings.Repeat("d", 64))
	if _, err := mutateUpgradeRepair(false, func(r *upgradeRepair) error { r.DispatchThread = "different-main-chat"; return nil }); err != nil {
		t.Fatal(err)
	}
	cycle := maintenance.Cycle{ID: strings.Repeat("a", 32), Mode: "full", Result: "completed", StartedAt: r.CreatedAt + 1}
	verifyUpgradeRepairCycle(scope, thread, turn, cycle, strings.Repeat("d", 64))
	state, _ := currentUpgradeRepair(false)
	if state.Status == "verified" {
		t.Fatal("foreground cycle established scheduled acceptance")
	}
	if err := admitHeartbeatHostRun(scope, thread, turn, cycle.ID); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.RecordLifecycle(scope, heartbeatHostAttempt(thread, turn), "trigger", "observed", maintenance.RuntimeObservation{Product: "codex"}); err != nil {
		t.Fatal(err)
	}
	cycle.StartedAt = time.Now().Add(time.Millisecond).UnixMilli()
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcript, nil, 0600); err != nil {
		t.Fatal(err)
	}
	appendHeartbeatHostCommand(t, transcript, turn, "eigenflux heartbeat plan --format agent", "completed", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle "+cycle.ID+" --stage feed --result completed")
	for _, bad := range []maintenance.Cycle{
		{ID: cycle.ID, Mode: "baseline", Result: "completed", StartedAt: cycle.StartedAt},
		{ID: cycle.ID, Mode: "full", Result: "failed", StartedAt: cycle.StartedAt},
		{ID: cycle.ID, Mode: "full", Result: "completed", StartedAt: r.CreatedAt - 1},
		{ID: strings.Repeat("b", 32), Mode: "full", Result: "completed", StartedAt: cycle.StartedAt},
	} {
		verifyUpgradeRepairCycle(scope, thread, turn, bad, strings.Repeat("d", 64), transcript)
		state, _ = currentUpgradeRepair(false)
		if state.Status == "verified" {
			t.Fatalf("accepted unrelated cycle %+v", bad)
		}
	}
	verifyUpgradeRepairCycle(scope, thread, turn, cycle, strings.Repeat("e", 64), transcript)
	state, _ = currentUpgradeRepair(false)
	if state.Status == "verified" {
		t.Fatal("old trusted Hook accepted a new guard definition")
	}
	verifyUpgradeRepairCycle(scope, thread, turn, cycle, strings.Repeat("d", 64), transcript)
	state, _ = currentUpgradeRepair(false)
	if state.Status != "verified" {
		t.Fatalf("real trigger did not finish acceptance: %+v", state)
	}
}

func TestUpgradeRepairCorruptionPreserved(t *testing.T) {
	runtimeTestConfig(t, "https://repair.example.invalid", true)
	if _, err := currentUpgradeRepair(true); err != nil {
		t.Fatal(err)
	}
	path, _, _ := upgradeRepairPath()
	body := []byte(`{"version":99}`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := currentUpgradeRepair(true); err == nil {
		t.Fatal("corruption reset receipt")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(body) {
		t.Fatal("corruption overwritten")
	}
}

func TestUpgradeRepairGatePreservesDisabledSetup(t *testing.T) {
	runtimeTestConfig(t, "https://repair.example.invalid", true)
	old := clientMeta
	defer func() { clientMeta = old }()
	clientMeta.Host, clientMeta.Mode = "codex", "skill"
	if !codexUpgradeRepairEnabled() {
		t.Fatal("Codex skill upgrade omitted")
	}
	t.Setenv("EIGENFLUX_SKIP_AGENT_SETUP", "1")
	if codexUpgradeRepairEnabled() {
		t.Fatal("disabled setup resumed")
	}
}

func TestUpgradeRepairNotificationOnlyFallbackIsDurableAndDoesNotStopWatch(t *testing.T) {
	w := newNotificationWatch(t, "https://repair.example.invalid")
	oldMeta := clientMeta
	t.Cleanup(func() { clientMeta = oldMeta })
	clientMeta.Host, clientMeta.Mode = "codex", "skill"
	for i := 0; i < 3; i++ {
		if err := w.tryUpgradeRepair(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	pending, delivered := w.desktop.Counts()
	if pending != 1 || delivered != 0 {
		t.Fatalf("fallback repeated: %d/%d", pending, delivered)
	}
	r, err := currentUpgradeRepair(false)
	if err != nil || r.Status != "pending" || r.Attempts != 0 {
		t.Fatalf("guessed owner binding: %+v %v", r, err)
	}
	if err := w.notificationIdentityOK(); err != nil {
		t.Fatal("repair broke notification watch", err)
	}
}

func TestUpgradeRepairAcknowledgedPromptDoesNotDuplicateDesktop(t *testing.T) {
	w := newNotificationWatch(t, "https://repair.example.invalid")
	oldMeta := clientMeta
	t.Cleanup(func() { clientMeta = oldMeta })
	clientMeta.Host, clientMeta.Mode = "codex", "skill"
	r, _ := currentUpgradeRepair(true)
	r, err := reportUpgradeRepair(r.ID, "user_action", []string{"hook_review", "host_restart"})
	if err != nil {
		t.Fatal(err)
	}
	if err = acknowledgeUpgradeRepair(r.ID, r.NoticeID); err != nil {
		t.Fatal(err)
	}
	if err = w.tryUpgradeRepair(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending, delivered := w.desktop.Counts()
	if pending+delivered != 0 {
		t.Fatal("confirmed owner prompt also sent a toast")
	}
}

func TestUpgradeRepairHeartbeatConsolidatesRecoveryPrompt(t *testing.T) {
	plan := heartbeatPlan{Notifications: &upgradeNotificationStatus{Status: "permission_or_setup_required"}, OuterLoop: &updateWatchRecovery{Status: "pending", Action: "old recovery", Notification: "required", UpgradeRepair: &upgradeRepair{ID: strings.Repeat("a", 32), Status: "user_action", Instruction: upgradeRepairInstruction}}}
	text := renderHeartbeatPlanForAgent(plan)
	if strings.Contains(text, "Notify the owner of this new or changed recovery action separately") || !strings.Contains(text, "one scoped upgrade-repair action summary") {
		t.Fatal("split owner prompts", text)
	}
	if strings.Contains(text, "Report missing permission or setup separately") || !strings.Contains(text, "same upgrade-repair summary") {
		t.Fatal("notification setup escaped combined action prompt", text)
	}
}

func TestUpgradeRepairSkillsFailureDistinguishesRetryFromLocalConflict(t *testing.T) {
	for _, tc := range []struct{ reason, status string }{{"skills_sync_failed", "pending"}, {"skills_local_modified", "blocked"}, {"skills_target_unresolved", "blocked"}} {
		t.Run(tc.reason, func(t *testing.T) {
			runtimeTestConfig(t, "https://repair.example.invalid", true)
			r, _ := currentUpgradeRepair(true)
			out, err := recordUpgradeSkillsFailure(r.ID, &upgradeSkillsError{Reason: tc.reason, Err: errors.New("private error")})
			if err != nil || out.Status != tc.status || len(out.Blockers) != 1 || out.Blockers[0] != "skills_unavailable" {
				t.Fatalf("unclassified failure %+v %v", out, err)
			}
			if out.Status == "pending" && out.NextAttempt <= time.Now().UnixMilli() {
				t.Fatal("transient sync failure lost retry")
			}
			if out.Status == "pending" && out.Notification != "" {
				t.Fatal("transient network failure requires user action")
			}
		})
	}
}

func TestUpgradeRepairSkillsTargetFailureUsesExistingWrappedClassification(t *testing.T) {
	runtimeTestConfig(t, "https://repair.example.invalid", true)
	r, _ := currentUpgradeRepair(true)
	out, err := recordUpgradeSkillsFailure(r.ID, &upgradeSkillsError{Reason: "skills_sync_failed", FailureReason: "skills_target_unresolved", Err: errors.New("private path")})
	if err != nil || out.Status != "blocked" || out.Notification != "required" {
		t.Fatalf("permanent target loops silently: %+v %v", out, err)
	}
}

func TestUpgradeRepairNewBlockerIsNotSuppressedByOldFallback(t *testing.T) {
	w := newNotificationWatch(t, "https://repair.example.invalid")
	r, _ := currentUpgradeRepair(true)
	if err := w.repairFallback(r); err != nil {
		t.Fatal(err)
	}
	r, err := reportUpgradeRepair(r.ID, "user_action", []string{"hook_review"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := w.repairFallback(r); err != nil {
			t.Fatal(err)
		}
	}
	pending, _ := w.desktop.Counts()
	if pending != 2 {
		t.Fatalf("new state dropped or same state repeated: %d", pending)
	}
}

func TestUpgradeRepairBusyOwnerKeepsAutomaticRetry(t *testing.T) {
	runtimeTestConfig(t, "https://repair.example.invalid", true)
	r, _ := currentUpgradeRepair(true)
	_, err := mutateUpgradeRepair(false, func(r *upgradeRepair) error { r.Status = "submitted"; r.Attempts = 8; return nil })
	if err != nil {
		t.Fatal(err)
	}
	out, err := finishUpgradeRepairDispatch(r.ID, dispatch.ErrMainNoticeBusy)
	if err != nil || out.Status != "pending" || out.NextAttempt <= time.Now().Add(time.Minute).UnixMilli() {
		t.Fatalf("busy owner forced manual recovery: %+v %v", out, err)
	}
}

func TestUpgradeRepairContextCancelsOnBindingDrift(t *testing.T) {
	w := newCommissionWatch(t, "https://repair.example.invalid", "commission_order")
	old := clientMeta
	t.Cleanup(func() { clientMeta = old })
	clientMeta.Host, clientMeta.Mode = "codex", "skill"
	ctx, cancel := w.upgradeRepairContext(context.Background(), *w.binding)
	defer cancel()
	changed := *w.binding
	changed.Revision = strings.Repeat("b", 32)
	if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), changed); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("binding drift left maintenance active")
	}
}

func TestUpgradeRepairSchedulerTargetChangeInvalidatesOldAcknowledgment(t *testing.T) {
	_, server := runtimeTestConfig(t, "https://repair.example.invalid", true)
	r, _ := currentUpgradeRepair(true)
	scope, _ := maintenanceScope(server)
	bindUpgradeRepairScheduler(scope, guardTestThread, strings.Repeat("d", 64))
	r, err := reportUpgradeRepair(r.ID, "user_action", []string{"hook_review"})
	if err != nil {
		t.Fatal(err)
	}
	oldNotice := r.NoticeID
	if err := acknowledgeUpgradeRepair(r.ID, oldNotice); err != nil {
		t.Fatal(err)
	}
	bindUpgradeRepairScheduler(scope, "12345678-1234-1234-1234-123456789abc", strings.Repeat("d", 64))
	r, err = currentUpgradeRepair(false)
	if err != nil || r.NoticeID == oldNotice || r.Notification != "required" {
		t.Fatalf("different target inherited trust notice: %+v %v", r, err)
	}
	if err := acknowledgeUpgradeRepair(r.ID, oldNotice); err == nil {
		t.Fatal("old target acknowledged changed Hook")
	}
}

func TestUpgradeRepairBackgroundScopeCannotFollowChangedForegroundDefault(t *testing.T) {
	runtimeTestConfig(t, "https://repair.example.invalid", true)
	if _, err := mutateUpgradeRepairScoped(strings.Repeat("a", 64), true, nil); err == nil {
		t.Fatal("background identity followed unrelated active account")
	}
	path, _, err := upgradeRepairPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("foreign account request created", err)
	}
}
