package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/skills"
	"github.com/spf13/cobra"
)

func TestHeartbeatNarrowModesUseOnlyCentralSelectedRules(t *testing.T) {
	for _, mode := range []string{"control", "maintenance"} {
		t.Run(mode, func(t *testing.T) {
			requests := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.URL.Path)
				if r.URL.Path == "/api/v2/agent-context" {
					_, _ = w.Write([]byte(`{"code":0,"data":{"context_revision":1}}`))
					return
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			}))
			defer server.Close()
			cfg, _ := runtimeTestConfig(t, server.URL, true)
			_ = cfg.SetKV("auto_cli_update", "false")
			clientMeta.Host, clientMeta.Mode = "codex", "skill"
			rules := installHeartbeatTestRules(t)
			command := &cobra.Command{}
			command.Flags().Bool(mode+"-only", true, "")
			old := formatFlag
			formatFlag = "json"
			defer func() { formatFlag = old }()
			text, err := captureHeartbeatStdout(t, func() error { return heartbeatPlanCmd.RunE(command, nil) })
			if err != nil {
				t.Fatal(err)
			}
			var plan heartbeatPlan
			if err := json.Unmarshal([]byte(text), &plan); err != nil {
				t.Fatal(err)
			}
			wantRule, wantStage := "maintenance.md", "maintenance"
			if mode == "control" {
				wantRule, wantStage = "commands.md", "commands"
			}
			if len(plan.RuleSources) != 2 || plan.RuleSources[0] != filepath.Join(rules, "ef-profile", "references", "runtime-model.md") || plan.RuleSources[1] != filepath.Join(rules, "ef-broadcast", "references", wantRule) || len(plan.ExecutionOrder) != 1 || plan.ExecutionOrder[0] != wantStage {
				t.Fatalf("mode leaked other stages: %+v", plan)
			}
			if plan.AgentPrompt != renderHeartbeatPlanForAgent(plan) {
				t.Fatal("JSON and Agent modes differ")
			}
			if mode == "control" {
				if plan.PluginMaintenance.Due || plan.SchedulerMigration != "" || plan.SkillsReadReceipt != nil {
					t.Fatal("control requested maintenance")
				}
				for _, path := range requests {
					if path != "/api/v2/agent-context" {
						t.Fatalf("control made maintenance request %s", path)
					}
				}
			}
		})
	}
}

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

func TestNarrowPlanFlagsRejectAmbiguousMode(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("control-only", true, "")
	cmd.Flags().Bool("maintenance-only", true, "")
	if err := heartbeatPlanCmd.RunE(cmd, nil); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("ambiguous flags: %v", err)
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

func TestDisabledSkillsSyncQuietlySkipsUnavailableLocalRules(t *testing.T) {
	for _, state := range []string{"missing", "modified", "unsigned"} {
		t.Run(state, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				http.Error(w, "unexpected request", 500)
			}))
			defer server.Close()
			runtimeTestConfig(t, server.URL, true)
			dir := installHeartbeatTestRules(t)
			switch state {
			case "missing":
				t.Setenv("EIGENFLUX_SKILLS_DIR", filepath.Join(t.TempDir(), "missing"))
			case "modified":
				if err := os.WriteFile(filepath.Join(dir, "ef-broadcast", "SKILL.md"), []byte("modified"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unsigned":
				manifest, err := skills.ReadLocalManifest(dir)
				if err != nil {
					t.Fatal(err)
				}
				manifest.Signature = ""
				if err := skills.WriteManifestAtomic(dir, manifest); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("EIGENFLUX_CDN_URL", server.URL)
			command := &cobra.Command{}
			command.Flags().Bool("if-stale", true, "")
			command.Flags().Bool("quiet", true, "")
			command.Flags().String("host", "codex", "")
			command.Flags().String("into", "", "")
			text, err := captureHeartbeatStdout(t, func() error { return skillsSyncCmd.RunE(command, nil) })
			if err != nil || text != "" || requests != 0 {
				t.Fatalf("quiet disabled sync: output=%q err=%v requests=%d", text, err, requests)
			}
			if _, err := localHeartbeatSkills("codex"); err == nil {
				t.Fatal("heartbeat accepted unavailable local rules")
			}
		})
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

func TestFullPlanCannotSuppressIndependentMaintenanceAndSlowRulesReceipt(t *testing.T) {
	var observed []maintenance.Event
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/agent-context" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"context_revision":1}}`))
			return
		}
		if r.URL.Path == "/api/v2/maintenance/events:batch" {
			var batch struct {
				Events []maintenance.Event `json:"events"`
			}
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
				t.Error(err)
			}
			observed = append(observed, batch.Events...)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer server.Close()
	_, name := runtimeTestConfig(t, server.URL, true)
	clientMeta.Host, clientMeta.Mode = "codex", "skill"
	installHeartbeatTestRules(t)
	old := formatFlag
	formatFlag = "json"
	defer func() { formatFlag = old }()
	run := func(command *cobra.Command) heartbeatPlan {
		t.Helper()
		text, err := captureHeartbeatStdout(t, func() error { return heartbeatPlanCmd.RunE(command, nil) })
		if err != nil {
			t.Fatal(err)
		}
		var p heartbeatPlan
		if err = json.Unmarshal([]byte(text), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := run(&cobra.Command{})
	second := run(&cobra.Command{})
	managedCommand := &cobra.Command{}
	managedCommand.Flags().Bool("watch-managed", true, "")
	managed := run(managedCommand)
	if !managed.WatchManaged || managed.PluginMaintenance.Status != "not_applicable" || managed.SchedulerMigration != "" {
		t.Fatal("watch-managed full plan retained a host maintenance owner")
	}
	for _, source := range managed.RuleSources {
		if strings.HasSuffix(source, "maintenance.md") {
			t.Fatal("watch-managed plan still loads maintenance rules")
		}
	}
	if !strings.Contains(managed.AgentPrompt, "owned exclusively by the watch maintenance-only handler") {
		t.Fatal("watch-managed owner missing from Agent prompt")
	}
	explicitMaintenance := &cobra.Command{}
	explicitMaintenance.Flags().Bool("maintenance-only", true, "")
	explicitMaintenance.Flags().Bool("watch-managed", true, "")

	if !maintenanceDue(name, time.Now()) {
		t.Fatal("full plan suppressed independent maintenance after a possible Feed failure")
	}
	if first.SkillsReadReceipt == nil || second.SkillsReadReceipt == nil {
		t.Fatal("verified rules missing read receipt")
	}
	for _, event := range observed {
		if event.Result == "rules_read" {
			t.Fatal("disk verification incorrectly claimed host read")
		}
	}
	report, _, err := rootCmd.Find([]string{"heartbeat", "maintenance-report"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(first.SkillsReadReceipt)
	report.SetIn(bytes.NewReader(body))
	_ = report.Flags().Set("stdin", "true")
	if _, err := captureHeartbeatStdout(t, func() error { return report.RunE(report, nil) }); err != nil {
		t.Fatalf("slow same-revision Agent receipt rejected: %v", err)
	}
	found := false
	for _, event := range observed {
		if event.Result == "rules_read" && event.AttemptID == first.SkillsReadReceipt.AttemptID {
			found = true
		}
	}
	if !found {
		t.Fatal("actual host receipt was not observed")
	}
	maintenancePlan := run(explicitMaintenance)
	if maintenancePlan.WatchManaged || maintenancePlan.PluginMaintenance.Status == "not_applicable" {
		t.Fatal("watch-managed flag suppressed explicit maintenance")
	}
	if maintenanceDue(name, time.Now()) {
		t.Fatal("dedicated maintenance did not throttle repeat triggers")
	}
	credentials, _ := auth.LoadV2Credentials(name)
	credentials.PrincipalID = "principal-1"
	if err := auth.SaveV2Credentials(name, credentials); err != nil {
		t.Fatal(err)
	}
	binding, err := watchBindingIdentity()
	if err != nil {
		t.Fatal(err)
	}
	binding.Revision = strings.Repeat("a", 32)
	binding.Events = []string{"pm_push"}
	if err := dispatch.WriteJSON(dispatch.BindingPath(binding.Home, binding.Server), binding); err != nil {
		t.Fatal(err)
	}
	pmManaged := run(managedCommand)
	if pmManaged.WatchManaged || pmManaged.PluginMaintenance.Status == "not_applicable" || strings.Contains(strings.Join(pmManaged.ExecutionOrder, ","), "communication") {
		t.Fatalf("PM-only ownership suppressed maintenance or duplicated PM: %+v", pmManaged)
	}
	if !strings.Contains(pmManaged.SchedulerLauncher, "--watch-managed") || pmManaged.SchedulerMigration != "" {
		t.Fatal("companion launcher lost ownership")
	}
	binding.Events = []string{"pm_push", "maintenance_due", "control_pending"}
	if err := dispatch.WriteJSON(dispatch.BindingPath(binding.Home, binding.Server), binding); err != nil {
		t.Fatal(err)
	}
	allManaged := run(managedCommand)
	if !allManaged.WatchManaged || allManaged.PluginMaintenance.Status != "not_applicable" || strings.Contains(strings.Join(allManaged.ExecutionOrder, ","), "commands") {
		t.Fatal("explicit maintenance/control ownership was lost")
	}
}
