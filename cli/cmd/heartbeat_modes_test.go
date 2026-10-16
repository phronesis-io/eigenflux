package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/skills"
	"github.com/spf13/cobra"
)

func TestDisabledSkillsSyncRejectsModifiedOrUnsignedRules(t *testing.T) {
	runtimeTestConfig(t, "http://127.0.0.1:1", true)
	dir := installHeartbeatTestRules(t)
	if _, err := localHeartbeatSkills("codex"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "ef-broadcast", "SKILL.md")
	if err := os.WriteFile(file, []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := localHeartbeatSkills("codex"); err == nil {
		t.Fatal("locally edited signed rules accepted")
	}
	m, _ := skills.ReadLocalManifest(dir)
	m.Signature = ""
	_ = skills.WriteManifestAtomic(dir, m)
	if _, err := localHeartbeatSkills("codex"); err == nil {
		t.Fatal("unsigned local rules accepted")
	}
}

func TestAutomaticSkillsSyncHonorsDisabledSwitchWithoutCDN(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; http.Error(w, "unexpected request", 500) }))
	defer server.Close()
	runtimeTestConfig(t, server.URL, true)
	installHeartbeatTestRules(t)
	t.Setenv("EIGENFLUX_CDN_URL", server.URL)
	command := &cobra.Command{}
	command.Flags().Bool("if-stale", true, "")
	command.Flags().Bool("quiet", true, "")
	command.Flags().String("host", "codex", "")
	command.Flags().String("into", "", "")
	old := formatFlag
	formatFlag = "json"
	defer func() { formatFlag = old }()
	_, err := captureHeartbeatStdout(t, func() error { return skillsSyncCmd.RunE(command, nil) })
	if err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatal("disabled automatic sync contacted CDN")
	}
}

func TestAutomaticSkillSwitchUsesServerOverride(t *testing.T) {
	cfg, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	if err := cfg.SetKV(autoSkillSyncKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetServerKV(server, autoSkillSyncKey, "false"); err != nil {
		t.Fatal(err)
	}
	maybeSyncSkills(cfg)
	if _, err := os.Stat(filepath.Join(config.HomeDir(), skills.AutoSyncStateFile)); !os.IsNotExist(err) {
		t.Fatalf("disabled server attempted auto sync: %v", err)
	}
}

func TestMaintenanceObservationDoesNotFollowConcurrentAccountSwitch(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	scope, err := maintenanceScope(server)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := auth.LoadV2Credentials(server)
	if err != nil {
		t.Fatal(err)
	}
	credentials.AgentID = "another-agent"
	if err := auth.SaveV2Credentials(server, credentials); err != nil {
		t.Fatal(err)
	}
	event := maintenance.NewEvent(maintenance.NewID(), "skills", "auto", "install", "installed")
	if recordMaintenanceEventAtScope(scope, event) == nil {
		t.Fatal("in-flight observation followed new account")
	}
	next, err := maintenanceScope(server)
	if err != nil {
		t.Fatal(err)
	}
	state, err := maintenance.Snapshot(next)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Events) != 0 {
		t.Fatal("old operation contaminated new account queue")
	}
}

func TestHeartbeatReleaseExcludesUnpublishedModes(t *testing.T) {
	for _, name := range []string{"watch-managed", "maintenance-only", "control-only", "shell"} {
		if heartbeatPlanCmd.Flags().Lookup(name) != nil {
			t.Fatalf("unpublished heartbeat flag %s remains registered", name)
		}
	}
	for _, child := range heartbeatCmd.Commands() {
		if child.Name() == "migrate" {
			t.Fatal("scheduler migration command remains registered")
		}
	}
}
