package cmd

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
)

func TestNativeRetryReachesWatchMaintenanceSchedule(t *testing.T) {
	for _, status := range []string{"blocked", "failed", "restart_required", "updating", "loaded", "not_installed"} {
		t.Run(status, func(t *testing.T) {
			cfg, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
			clientMeta = client.Meta{Host: "openclaw", Mode: "plugin"}
			p := pluginMaintenanceForHost("openclaw", "plugin", cfg)
			p.Status, p.CheckedAt = status, time.Now()
			if err := saveMaintenance(maintenancePath("plugin-openclaw-native"), p); err != nil {
				t.Fatal(err)
			}
			markMaintenanceAttempt(server)
			if maintenanceDue(server, p.CheckedAt.Add(4*time.Minute)) {
				t.Fatal("retried before the five-minute boundary")
			}
			wantRetry := status != "loaded" && status != "not_installed"
			if got := maintenanceDue(server, p.CheckedAt.Add(6*time.Minute)); got != wantRetry {
				t.Fatalf("six-minute retry=%v want=%v", got, wantRetry)
			}
			if !maintenanceDue(server, p.CheckedAt.Add(25*time.Hour)) {
				t.Fatal("daily maintenance never becomes due")
			}
			if err := cfg.SetKV("auto_plugin_update", "false"); err != nil {
				t.Fatal(err)
			}
			if maintenanceDue(server, p.CheckedAt.Add(6*time.Minute)) {
				t.Fatal("disabled native updater bypassed daily schedule")
			}
		})
	}
}

func TestNativeRetryScheduleUsesRequestedServer(t *testing.T) {
	cfg, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	clientMeta = client.Meta{Host: "openclaw", Mode: "plugin"}
	if err := cfg.AddServer("other", "http://127.0.0.1:2"); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveV2Credentials("other", &auth.V2Credentials{AgentID: "agent-other"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, entry := range []struct{ server, status string }{{server, "failed"}, {"other", "loaded"}} {
		p := pluginMaintenance{Context: pluginContext(), Status: entry.status, CheckedAt: now.Add(-6 * time.Minute)}
		if err := saveMaintenance(maintenancePathForServer("plugin-openclaw-native", entry.server), p); err != nil {
			t.Fatal(err)
		}
		markMaintenanceAttempt(entry.server)
	}
	if !maintenanceDue(server, now) || maintenanceDue("other", now) {
		t.Fatal("retry schedule crossed server receipt scopes")
	}
}

func TestOpenClawNativeMaintenanceSchedulesOnceAndSkipsAgent(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	count := 0
	old := launchOpenClawUpdate
	launchOpenClawUpdate = func(p pluginMaintenance) error { count++; return nil }
	t.Cleanup(func() { launchOpenClawUpdate = old })
	p := pluginMaintenanceForHost("openclaw", "plugin", cfg)
	if p.Executor != "cli" || !p.Due {
		t.Fatalf("native updater omitted: %+v", p)
	}
	p = scheduleOpenClawUpdate(p)
	if p.Status != "updating" || p.Due || count != 1 {
		t.Fatalf("worker not scheduled: %+v", p)
	}
	p = scheduleOpenClawUpdate(pluginMaintenanceForHost("openclaw", "plugin", cfg))
	if p.Status != "updating" || count != 1 {
		t.Fatalf("worker repeated: %+v", p)
	}
	prompt := renderHeartbeatPlanForAgent(heartbeatPlan{PluginMaintenance: p})
	if !strings.Contains(prompt, "Do not run plugin discovery, installation, restart, or plugin-check") {
		t.Fatal("Agent was allowed to duplicate native maintenance")
	}
}

func TestNativeCacheDoesNotAttestLiveLoadingAndRetriesFailures(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	p := pluginMaintenanceForHost("openclaw", "plugin", cfg)
	p.Status = "loaded"
	p.InstalledVersion = "0.0.46"
	p.LoadedVersion = "0.0.46"
	p.LatestVersion = "0.0.46"
	p.CheckedAt = time.Now()
	p.Scope = "installation"
	if err := saveMaintenance(maintenancePath("plugin-openclaw-native"), p); err != nil {
		t.Fatal(err)
	}
	got := pluginMaintenanceForHost("openclaw", "plugin", cfg)
	if got.Due || got.Status != "not_due" || got.LoadedVersion != "" || got.InstalledVersion != "" {
		t.Fatalf("cached runtime reused: %+v", got)
	}
	p.Status = "blocked"
	p.Error = "busy"
	p.CheckedAt = time.Now().Add(-6 * time.Minute)
	if err := saveMaintenance(maintenancePath("plugin-openclaw-native"), p); err != nil {
		t.Fatal(err)
	}
	if got := pluginMaintenanceForHost("openclaw", "plugin", cfg); !got.Due {
		t.Fatal("failure delayed for 24 hours")
	}
	_ = cfg.SetKV("auto_plugin_update", "false")
	if got := pluginMaintenanceForHost("openclaw", "plugin", cfg); got.Executor != "" || got.Status != "not_applicable" {
		t.Fatal("disabled native updater scheduled")
	}
}

func TestNativeWorkerLaunchFailureIsConcreteAndRetryable(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	old := launchOpenClawUpdate
	launchOpenClawUpdate = func(p pluginMaintenance) error { return errors.New("launch denied") }
	t.Cleanup(func() { launchOpenClawUpdate = old })
	p := scheduleOpenClawUpdate(pluginMaintenanceForHost("openclaw", "plugin", cfg))
	if p.Status != "failed" || !strings.Contains(p.Error, "launch denied") {
		t.Fatalf("failure hidden: %+v", p)
	}
	if _, err := os.Stat(maintenancePath("plugin-openclaw-native")); err != nil {
		t.Fatal("failure was not persisted")
	}
}

func TestNativePendingAdoptionSurvivesWorkerRelaunch(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	p := pluginMaintenanceForHost("openclaw", "plugin", cfg)
	p.Status, p.CheckedAt, p.RestartPendingVersion = "restart_required", time.Now().Add(-6*time.Minute), "0.0.47"
	if err := saveMaintenance(maintenancePath("plugin-openclaw-native"), p); err != nil {
		t.Fatal(err)
	}
	old := launchOpenClawUpdate
	launchOpenClawUpdate = func(p pluginMaintenance) error {
		// The detached worker reads the updating receipt written before launch.
		got := openClawMaintenancePlan(pluginMaintenance{Context: p.Context})
		if got.Status != "updating" || got.RestartPendingVersion != "0.0.47" || got.LoadedVersion != "" {
			t.Fatalf("pending adoption was lost or became live evidence: %+v", got)
		}
		return nil
	}
	t.Cleanup(func() { launchOpenClawUpdate = old })
	got := scheduleOpenClawUpdate(pluginMaintenanceForHost("openclaw", "plugin", cfg))
	if got.Status != "updating" || got.RestartPendingVersion != "0.0.47" {
		t.Fatalf("pending upgrade was not carried into next worker: %+v", got)
	}
}

func TestPluginWorkerEnvironmentHasNoWatchHandoffMarkers(t *testing.T) {
	env := cleanPluginUpdateEnvironment([]string{"PATH=/bin", "EIGENFLUX_HOME=/stable", updateReexecEnv + "=1", updateAttemptEnv + "=old", watchSupervisedEnv + "=1", watchAgentEnv + "=old", watchPrincipalEnv + "=old"})
	if strings.Join(env, "|") != "PATH=/bin|EIGENFLUX_HOME=/stable" {
		t.Fatalf("worker inherited stale handoff: %v", env)
	}
}
