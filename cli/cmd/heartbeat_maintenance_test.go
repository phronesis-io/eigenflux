package cmd

import (
	"bytes"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/heartbeatmigration"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/skills"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestPluginMaintenanceRequiresLoadedEvidence(t *testing.T) {
	p := pluginMaintenance{Host: "codex", PluginID: "codex-eigenflux@eigenflux", Scope: "user", Status: "loaded", InstalledVersion: "0.1.8", LatestVersion: "0.1.8"}
	if err := validatePluginReceipt(p, "codex/1.0"); err == nil {
		t.Fatal("unloaded plugin accepted as loaded")
	}
	p.Status = "restart_required"
	if err := validatePluginReceipt(p, "codex/1.0"); err != nil {
		t.Fatal(err)
	}
	p.Status, p.LoadedVersion = "loaded", "0.1.8"
	if err := validatePluginReceipt(p, "codex/1.0"); err != nil {
		t.Fatal(err)
	}
	if err := validatePluginReceipt(p, "openclaw/2026.5.7"); err == nil {
		t.Fatal("cross-host update accepted")
	}
	p.LatestVersion = "0.1.9"
	if err := validatePluginReceipt(p, "codex/1.0"); err == nil {
		t.Fatal("outdated installed version accepted")
	}
}

func TestHeartbeatKeepsOnlyCompatibleLocalRules(t *testing.T) {
	dir := installHeartbeatTestRules(t)
	if !compatibleLocalHeartbeatRules(dir, "0.0.47") {
		t.Fatal("compatible installed rules rejected")
	}
	m, _ := skills.ReadLocalManifest(dir)
	m.MinCLIVersion = "0.0.48"
	if err := skills.WriteManifestAtomic(dir, m); err != nil {
		t.Fatal(err)
	}
	if compatibleLocalHeartbeatRules(dir, "0.0.47") {
		t.Fatal("incompatible local rules accepted")
	}
	if compatibleLocalHeartbeatRules(t.TempDir(), "0.0.48") {
		t.Fatal("missing rules accepted")
	}
}

func TestHeartbeatReexecPreservesIdentityEnvironment(t *testing.T) {
	tempHome(t)
	env := []string{"EIGENFLUX_HOME=/wrong", "EIGENFLUX_MODE=skill", "EIGENFLUX_HOST=codex/1", "EIGENFLUX_MODEL=model", "EIGENFLUX_UPDATE_REEXEC=0", "PATH=/bin"}
	got := heartbeatReexecEnvironment(env)
	want := append(append([]string(nil), env[1:4]...), "PATH=/bin", updateReexecEnv+"=1", "EIGENFLUX_HOME="+config.HomeDir())
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reexec identity mismatch: %v", got)
	}
	args := heartbeatReexecArgs([]string{"heartbeat", "plan", "--format", "json", "--"}, "/stable/.eigenflux", "original-server")
	if !reflect.DeepEqual(args, []string{"heartbeat", "plan", "--format", "json", "--homedir", "/stable/.eigenflux", "--server", "original-server"}) {
		t.Fatalf("reexec failed to pin identity: %v", args)
	}
}

func TestMigrationCommandsPersistOnlyAfterVerifiedReadback(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	oldFormat := formatFlag
	formatFlag = "json"
	t.Cleanup(func() { formatFlag = oldFormat })
	planCmd, _, err := rootCmd.Find([]string{"heartbeat", "migrate", "plan"})
	if err != nil {
		t.Fatal(err)
	}
	verifyCmd, _, err := rootCmd.Find([]string{"heartbeat", "migrate", "verify"})
	if err != nil {
		t.Fatal(err)
	}
	in := heartbeatmigration.Inventory{Complete: true, Tasks: []heartbeatmigration.Task{{ID: "original", Name: "old", Owner: "eigenflux", Purpose: "heartbeat", Home: config.HomeDir(), Server: server, Prompt: "static rules", Schedule: "2h", Status: "PAUSED", ThreadID: "original-thread"}}}
	b, _ := json.Marshal(in)
	planCmd.SetIn(bytes.NewReader(b))
	_ = planCmd.Flags().Set("stdin", "true")
	out, err := captureHeartbeatStdout(t, func() error { return planCmd.RunE(planCmd, nil) })
	if err != nil {
		t.Fatal(err)
	}
	var record migrationRecord
	if err = json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(migrationReceiptPath(record.Host)); !os.IsNotExist(err) {
		t.Fatal("plan alone recorded migration success")
	}
	_ = verifyCmd.Flags().Set("stdin", "true")
	for _, valid := range []bool{false, true} {
		actual := in
		actual.Tasks = append([]heartbeatmigration.Task(nil), in.Tasks...)
		if valid {
			actual = record.Plan.After
		}
		b, _ = json.Marshal(map[string]interface{}{"plan_id": record.ID, "complete": true, "tasks": actual.Tasks})
		verifyCmd.SetIn(bytes.NewReader(b))
		out, err = captureHeartbeatStdout(t, func() error { return verifyCmd.RunE(verifyCmd, nil) })
		if valid && (err != nil || !strings.Contains(out, "verified")) {
			t.Fatalf("valid readback failed: %s %v", out, err)
		}
		if !valid && err == nil {
			t.Fatal("unchanged old prompt accepted")
		}
	}
	b, err = os.ReadFile(migrationReceiptPath(record.Host))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &record); err != nil || record.Verified.IsZero() {
		t.Fatal("missing verification receipt")
	}
	scope, scopeErr := maintenanceScope(server)
	if scopeErr != nil {
		t.Fatal(scopeErr)
	}
	observation, observeErr := maintenance.LastAttempt(scope, "scheduler")
	if observeErr != nil {
		t.Fatal(observeErr)
	}
	if observation.AttemptID != record.ID || observation.Result != "verified" || observation.Phase != "migration" {
		t.Fatalf("missing successful scheduler observation: %+v", observation)
	}
}
