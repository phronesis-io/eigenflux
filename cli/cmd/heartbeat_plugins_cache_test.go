package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPluginReceiptRetainsOriginatingPlanContext(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	clientMeta.Host, clientMeta.Mode = "codex", "plugin"
	plan := pluginMaintenanceForHost("codex", "plugin", cfg)
	if plan.Context == "" {
		t.Fatal("plan omitted discovery context")
	}
	plan.Scope, plan.Status = "user", "not_installed"
	// The Agent can execute the native manager and report from a different cwd.
	t.Chdir(t.TempDir())
	cmd, _, err := rootCmd.Find([]string{"heartbeat", "plugin-check"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(plan)
	cmd.SetIn(bytes.NewReader(b))
	_ = cmd.Flags().Set("stdin", "true")
	if _, err := captureHeartbeatStdout(t, func() error { return cmd.RunE(cmd, nil) }); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(maintenancePath("plugin-codex"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt pluginMaintenance
	if err := json.Unmarshal(b, &receipt); err != nil || receipt.Context != plan.Context {
		t.Fatalf("lost plan context: %+v %v", receipt, err)
	}
	if !pluginMaintenanceForHost("codex", "plugin", cfg).Due {
		t.Fatal("another workspace reused discovery")
	}
	t.Chdir(plan.Context)
	if pluginMaintenanceForHost("codex", "plugin", cfg).Due {
		t.Fatal("originating plugin could not reuse completed discovery")
	}
}

func TestAgentPlanIncludesDiscoveryScope(t *testing.T) {
	p := heartbeatPlan{PluginMaintenance: pluginMaintenance{Host: "codex", Scope: "user", Status: "check_required", LatestVersion: "1.2.3"}}
	out := renderHeartbeatPlanForAgent(p)
	for _, field := range []string{`"scope":"user"`, `"latest_version":"1.2.3"`, `"due":false`, `"status":"check_required"`} {
		if !strings.Contains(out, field) {
			t.Fatalf("missing plugin discovery field %s", field)
		}
	}
}

func TestPluginCacheIsDiscoveryOnlyAndWorkspaceBound(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	for _, status := range []string{"loaded", "restart_required"} {
		p := pluginMaintenance{Host: "codex", PluginID: heartbeatPluginID("codex"), Scope: "user", Context: pluginContext(), Status: status, InstalledVersion: "1.2.3", LatestVersion: "1.2.3", LoadedVersion: "1.2.3", CheckedAt: time.Now()}
		if err := saveMaintenance(maintenancePath("plugin-codex"), p); err != nil {
			t.Fatal(err)
		}
		got := pluginMaintenanceForHost("codex", "skill", cfg)
		if got.Due || got.Status != "check_required" || got.InstalledVersion != "" || got.LoadedVersion != "" || got.LatestVersion != p.LatestVersion {
			t.Fatalf("historical process state leaked: %+v", got)
		}
		p.Context = t.TempDir()
		if err := saveMaintenance(maintenancePath("plugin-codex"), p); err != nil {
			t.Fatal(err)
		}
		if got := pluginMaintenanceForHost("codex", "skill", cfg); !got.Due {
			t.Fatalf("different workspace reused cached scope: %+v", got)
		}
	}
	for _, mode := range []string{"", "unknown"} {
		if got := pluginMaintenanceForHost("codex", mode, cfg); got.Due || got.Status != "not_applicable" {
			t.Fatalf("automatic maintenance in mode %q: %+v", mode, got)
		}
	}
	if err := cfg.SetKV("auto_plugin_update", "false"); err != nil {
		t.Fatal(err)
	}
	if got := pluginMaintenanceForHost("codex", "skill", cfg); got.Due || got.Status != "not_applicable" {
		t.Fatalf("disabled plugin maintenance was offered: %+v", got)
	}
}

func TestPluginReceiptRequiresScope(t *testing.T) {
	p := pluginMaintenance{Host: "codex", PluginID: heartbeatPluginID("codex"), Status: "not_installed"}
	if err := validatePluginReceipt(p, "codex"); err == nil {
		t.Fatal("missing installation scope accepted")
	}
	p.Scope = "user"
	if err := validatePluginReceipt(p, "codex"); err != nil {
		t.Fatal(err)
	}
}

func TestSharedHeartbeatMaintenanceModes(t *testing.T) {
	cfg, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	for _, mode := range []string{"skill", "plugin"} {
		for _, host := range []string{"codex", "claude-code", "openclaw"} {
			p := pluginMaintenanceForHost(host, mode, cfg)
			if !p.Due || p.PluginID != heartbeatPluginID(host) {
				t.Fatalf("missing maintenance for %s/%s: %+v", host, mode, p)
			}
		}
	}
	for _, key := range []string{"auto_cli_update", "auto_plugin_update"} {
		if err := cfg.SetKV(key, "false"); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"skill", "plugin"} {
			if heartbeatMaintenanceEnabled(mode, cfg, key) {
				t.Fatalf("%s ignored in %s", key, mode)
			}
		}
		if err := cfg.SetServerKV(server, key, "true"); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"skill", "plugin"} {
			if !heartbeatMaintenanceEnabled(mode, cfg, key) {
				t.Fatalf("server override ignored for %s/%s", key, mode)
			}
		}
	}
}

func TestPluginMaintenanceCanWakeWithoutFeed(t *testing.T) {
	baseline := runtimeAccess{OnboardingState: "in_progress"}
	if !heartbeatWakeOnEmpty(baseline, pluginMaintenance{Due: true}) {
		t.Fatal("due maintenance dropped when Feed is empty")
	}
	if heartbeatWakeOnEmpty(baseline, pluginMaintenance{Status: "not_applicable"}) {
		t.Fatal("disabled maintenance woke incomplete onboarding")
	}
	if stages := heartbeatStages(baseline); len(stages) != 1 || stages[0] != "feed" {
		t.Fatal("maintenance wake bypassed onboarding restrictions")
	}
}

func TestCachedPluginLoadCheckDoesNotExtendDiscoveryTTL(t *testing.T) {
	runtimeTestConfig(t, "http://127.0.0.1:1", true)
	now := time.Now()
	p := pluginMaintenance{Host: "codex", PluginID: heartbeatPluginID("codex"), Scope: "user", Context: pluginContext(), LatestVersion: "1.2.3", CheckedAt: now.Add(-23 * time.Hour)}
	if err := saveMaintenance(maintenancePath("plugin-codex"), p); err != nil {
		t.Fatal(err)
	}
	if got := pluginDiscoveryTime(p, now); !got.Equal(p.CheckedAt) {
		t.Fatal("cached load evidence postponed discovery")
	}
	if got := pluginDiscoveryTime(p, now.Add(2*time.Hour)); !got.Equal(now.Add(2 * time.Hour)) {
		t.Fatal("expired discovery not refreshed")
	}
	p.LatestVersion = "1.2.4"
	if got := pluginDiscoveryTime(p, now); !got.Equal(now) {
		t.Fatal("new release discovery kept old timestamp")
	}
}
