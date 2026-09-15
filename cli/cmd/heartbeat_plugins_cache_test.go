package cmd

import (
	"strings"
	"testing"
	"time"
)

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
	for _, mode := range []string{"", "plugin", "unknown"} {
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
