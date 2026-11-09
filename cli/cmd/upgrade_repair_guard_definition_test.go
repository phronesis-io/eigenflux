package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpgradeRepairGuardBindingFailureIsRetryableAndCannotClaimPrepared(t *testing.T) {
	_, server := runtimeTestConfig(t, "https://repair.example.invalid", true)
	r, err := currentUpgradeRepair(true)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := maintenanceScope(server)
	if err != nil {
		t.Fatal(err)
	}
	path, _, _ := upgradeRepairPath()
	lock, err := lockWatchRecoveryNotice(path)
	if err != nil {
		t.Fatal(err)
	}
	err = bindUpgradeRepairScheduler(scope, guardTestThread, strings.Repeat("d", 64))
	lock.Release()
	if err == nil {
		t.Fatal("failed binding was reported successful")
	}
	if _, err := reportUpgradeRepair(r.ID, "awaiting_run", nil); err == nil {
		t.Fatal("lost target became an unattended-run wait")
	}
	if err := bindUpgradeRepairScheduler(scope, guardTestThread, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := reportUpgradeRepair(r.ID, "awaiting_run", nil); err != nil {
		t.Fatal("idempotent binding retry failed", err)
	}
}

func TestUpgradeRepairGuardBindingDoesNotRequireAnUpgradeRequest(t *testing.T) {
	_, server := runtimeTestConfig(t, "https://repair.example.invalid", true)
	scope, _ := maintenanceScope(server)
	if err := bindUpgradeRepairScheduler(scope, guardTestThread, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	path, _, _ := upgradeRepairPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("ordinary setup created upgrade intent", err)
	}
}

func TestUpgradeRepairChangedGuardDefinitionInvalidatesTrustAndAcceptance(t *testing.T) {
	_, server := runtimeTestConfig(t, "https://repair.example.invalid", true)
	scope, _ := maintenanceScope(server)
	r, err := currentUpgradeRepair(true)
	if err != nil {
		t.Fatal(err)
	}
	cli, codexHome := filepath.Join(t.TempDir(), "eigenflux"), t.TempDir()
	launcher, err := renderHeartbeatLauncher(cli, scope.Home, scope.Server, "skill", "posix")
	if err != nil {
		t.Fatal(err)
	}
	definitions := []string{}
	for _, suffix := range []string{"", " --watch-managed"} {
		prompt := heartbeatSchedulerPrompt(launcher + suffix)
		changed, err := installCodexGuard(codexHome, cli, scope.Home, scope.Server, guardTestThread, scope.AgentID, prompt)
		if err != nil || !changed {
			t.Fatalf("expected changed native definition %v %v", changed, err)
		}
		promptHash, err := codexGuardPromptHash(cli, scope.Home, scope.Server, prompt)
		if err != nil {
			t.Fatal(err)
		}
		definition, err := codexGuardDefinition(codexHome, cli, scope.Home, scope.Server, guardTestThread, scope.AgentID, promptHash)
		if err != nil {
			t.Fatal(err)
		}
		definitions = append(definitions, definition)
		if err := bindUpgradeRepairScheduler(scope, guardTestThread, definition); err != nil {
			t.Fatal(err)
		}
		r, err = reportUpgradeRepair(r.ID, "user_action", []string{"hook_review"})
		if err != nil || r.Notification != "required" {
			t.Fatalf("changed guard inherited old confirmation %+v %v", r, err)
		}
		if err := acknowledgeUpgradeRepair(r.ID, r.NoticeID); err != nil {
			t.Fatal(err)
		}
	}
	if definitions[0] == definitions[1] {
		t.Fatal("canonical prompt change retained definition")
	}
	oldCreated := r.CreatedAt
	_, err = mutateUpgradeRepair(false, func(r *upgradeRepair) error { r.Status = "verified"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	otherHome := t.TempDir()
	promptHash, _ := codexGuardPromptHash(cli, scope.Home, scope.Server, heartbeatSchedulerPrompt(launcher))
	definition, _ := codexGuardDefinition(otherHome, cli, scope.Home, scope.Server, guardTestThread, scope.AgentID, promptHash)
	if err := bindUpgradeRepairScheduler(scope, guardTestThread, definition); err != nil {
		t.Fatal(err)
	}
	r, err = currentUpgradeRepair(false)
	if err != nil || r.Status != "awaiting_run" || r.CreatedAt < oldCreated || r.CreatedAt > time.Now().UnixMilli() {
		t.Fatalf("new Codex store inherited old acceptance %+v %v", r, err)
	}
	raw, err := os.ReadFile(filepath.Join(codexHome, "hooks.json"))
	if err != nil || !strings.Contains(string(raw), "--guard-definition") {
		t.Fatal("Stop handler has no exact definition fence", err)
	}
}
