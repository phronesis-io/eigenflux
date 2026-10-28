package cmd

import (
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/skills"
	"reflect"
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
