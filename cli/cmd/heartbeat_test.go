package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/skills"
	"github.com/spf13/cobra"
)

func TestRenderHeartbeatPlanForAgentIsThinAndCurrent(t *testing.T) {
	plan := heartbeatPlan{
		HeartbeatContractVersion: heartbeatContractVersion,
		CLIVersion:               "0.0.34", SkillRevision: "rev-123", SkillsTarget: "/tmp/skills",
		Skills:             []string{"ef-broadcast", "ef-communication", "ef-future"},
		RuleSources:        []string{"/tmp/skills/ef-broadcast/SKILL.md", "/tmp/skills/ef-broadcast/references/attention.md"},
		CLIPrefix:          "eigenflux --homedir /tmp/home",
		SchedulerLauncher:  "eigenflux --homedir /tmp/home heartbeat plan --format agent",
		SchedulerMigration: "migrate owned task",
		Access:             runtimeAccess{Mode: "intent_aligned", OnboardingState: "completed"},
		ExecutionOrder:     []string{"commands", "feed", "attention", "communication", "publish", "settings_report"},
	}
	text := renderHeartbeatPlanForAgent(plan)
	for _, required := range []string{
		heartbeatContractVersion, "rev-123", "ef-future", "Freshly read, from disk",
		"commands → feed → attention → communication → publish → settings_report",
		"CLI prefix for every EigenFlux command in this cycle: eigenflux --homedir /tmp/home",
		"Never run a bare eigenflux command",
		"Apply runtime-model.md before subsequent CLI calls",
		"pass it through --runtime-model for each invocation",
		"If unavailable, keep it unset and continue permitted Feed work",
		"Apply the current Skills to each stage",
		"A Feed payload supplied by the host is this cycle's completed pull",
		"Follow the current Skills for onboarding restrictions and recovery",
		"Host harness output and notification requirements take precedence",
		"scheduler stores this fixed execution prompt",
		"Store it verbatim, without additions",
		"Even with no updates, return the complete required response (XML when prescribed)",
		"never an empty message or silence token",
		"Routine cycle completion alone does not warrant notification",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("heartbeat plan missing %q:\n%s", required, text)
		}
	}
}

func TestHeartbeatPlanJSONCarriesCurrentPromptAndAccessDecision(t *testing.T) {
	for _, baseline := range []bool{true, false} {
		name := "completed"
		if baseline {
			name = "baseline"
		}
		t.Run(name, func(t *testing.T) {
			contextRequests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/agent-context" {
					contextRequests++
					if baseline {
						w.WriteHeader(http.StatusConflict)
						_, _ = w.Write([]byte(`{"error":{"code":"ONBOARDING_REQUIRED","details":{"onboarding_state":"in_progress"}}}`))
						return
					}
					_, _ = w.Write([]byte(`{"code":0,"data":{"context_revision":1}}`))
					return
				}
				if baseline {
					t.Errorf("baseline made non-Feed business request: %s %s", r.Method, r.URL.Path)
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			}))
			defer server.Close()
			_, serverName := runtimeTestConfig(t, server.URL, true)
			version = "0.0.46"
			rulesDir := installHeartbeatTestRules(t)
			oldFormat := formatFlag
			formatFlag = "json"
			t.Cleanup(func() { formatFlag = oldFormat })
			text, err := captureHeartbeatStdout(t, func() error {
				return heartbeatPlanCmd.RunE(&cobra.Command{}, nil)
			})
			if err != nil {
				t.Fatal(err)
			}
			var plan heartbeatPlan
			if err := json.Unmarshal([]byte(text), &plan); err != nil {
				t.Fatalf("invalid JSON %q: %v", text, err)
			}
			wantStages := []string{"commands", "feed", "attention", "communication", "publish", "settings_report"}
			if baseline {
				wantStages = []string{"feed"}
			}
			if !reflect.DeepEqual(plan.ExecutionOrder, wantStages) || plan.WakeOnEmpty == baseline {
				t.Fatalf("incorrect central dispatch: %+v", plan)
			}
			if contextRequests != 1 {
				t.Fatalf("context fetched %d times", contextRequests)
			}
			if plan.AgentPrompt == "" || plan.AgentPrompt != renderHeartbeatPlanForAgent(plan) {
				t.Fatalf("JSON discarded or changed central prompt: %q", plan.AgentPrompt)
			}
			if !reflect.DeepEqual(plan.RuntimeReport.Missing, []string{"runtime_name", "mode", "model"}) ||
				!strings.Contains(plan.AgentPrompt, "missing: runtime_name, mode, model") {
				t.Fatalf("missing current runtime metadata not delivered: %+v", plan.RuntimeReport)
			}
			modelRule := filepath.Join(rulesDir, "ef-profile", "references", "runtime-model.md")
			if len(plan.RuleSources) == 0 || plan.RuleSources[0] != modelRule || !strings.Contains(plan.AgentPrompt, modelRule) {
				t.Fatalf("current model rule absent from required sources: %+v", plan.RuleSources)
			}
			if strings.Contains(plan.SchedulerLauncher, "EIGENFLUX_MODEL") {
				t.Fatal("scheduler must not freeze the current model")
			}
			if plan.SkillsTarget != rulesDir || !strings.Contains(plan.CLIPrefix, "--server "+shellQuote(serverName)) {
				t.Fatalf("host target/server lost: %+v", plan)
			}
		})
	}
}

func TestHeartbeatPlanFailsWithoutCurrentAccessOrRules(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		missingRules string
	}{
		{"invalid context", `{"code":0,"data":{}}`, ""},
		{"missing rules", `{"code":0,"data":{"context_revision":1}}`, "ef-communication/SKILL.md"},
		{"missing model rules", `{"code":0,"data":{"context_revision":1}}`, "ef-profile/references/runtime-model.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			runtimeTestConfig(t, server.URL, true)
			dir := installHeartbeatTestRules(t)
			if tc.missingRules != "" {
				if err := os.Remove(filepath.Join(dir, tc.missingRules)); err != nil {
					t.Fatal(err)
				}
			}
			oldFormat := formatFlag
			formatFlag = "agent"
			t.Cleanup(func() { formatFlag = oldFormat })
			var out bytes.Buffer
			command := &cobra.Command{}
			command.SetOut(&out)
			if err := heartbeatPlanCmd.RunE(command, nil); err == nil || out.Len() != 0 {
				t.Fatalf("invalid central input emitted a plan: err=%v output=%q", err, out.String())
			}
		})
	}
}

func installHeartbeatTestRules(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_SKILLS_DIR", dir)
	t.Setenv("EIGENFLUX_CDN_URL", "http://127.0.0.1:1")
	names := []string{"ef-broadcast", "ef-communication", "ef-profile"}
	for _, name := range names {
		path := filepath.Join(dir, name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("# "+name+"\nFollow the current central procedure.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	attention := filepath.Join(dir, "ef-broadcast", "references", "attention.md")
	if err := os.MkdirAll(filepath.Dir(attention), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attention, []byte("# Attention\nCentral Attention procedure.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	modelRule := filepath.Join(dir, "ef-profile", "references", "runtime-model.md")
	if err := os.MkdirAll(filepath.Dir(modelRule), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelRule, []byte("# Runtime Model Reporting\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := skills.GenerateManifest(dir, "0.0.46", "0.0.46", names, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := skills.WriteManifestAtomic(dir, manifest); err != nil {
		t.Fatal(err)
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return realDir
}

func captureHeartbeatStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	previous := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = previous }()
	runErr := run()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), runErr
}

func TestSchedulerMigrationUsesNativeHostOwnership(t *testing.T) {
	launcher := "eigenflux --homedir /stable heartbeat plan --format agent"
	tests := map[string][]string{
		"workbuddy/5.3.14": {"CronList/CronUpdate", "owned EigenFlux task"},
		"codex/1.0":        {"native automation", "owned EigenFlux task"},
		"hermes/0.20":      {"cron list/edit", "ownership marker", "matching Home"},
		"openclaw/1.0":     {"plugin owns scheduling", "do not create a second heartbeat"},
		"claude-code/1.0":  {"plugin owns scheduling", "do not create a second heartbeat"},
		"unknown/1.0":      {"official scheduler API", "proposed diff"},
	}
	for host, required := range tests {
		got := schedulerMigrationForHost(host, launcher)
		if !strings.Contains(got, launcher) {
			t.Fatalf("%s migration dropped launcher: %s", host, got)
		}
		for _, fragment := range required {
			if !strings.Contains(got, fragment) {
				t.Fatalf("%s migration missing %q: %s", host, fragment, got)
			}
		}
	}
}

func TestHeartbeatCommandsAreRegistered(t *testing.T) {
	if heartbeatPlanCmd.Parent() != heartbeatCmd || heartbeatCmd.Parent() != rootCmd {
		t.Fatal("heartbeat plan command is not registered under the root command")
	}
}

func TestHeartbeatPlanAcceptsLegacyEnvironmentMode(t *testing.T) {
	for _, mode := range []string{"plugin", "skill"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"code":0,"data":{"context_revision":1}}`))
			}))
			defer server.Close()
			_, serverName := runtimeTestConfig(t, server.URL, true)
			installHeartbeatTestRules(t)
			t.Setenv("EIGENFLUX_MODE", mode)
			oldFormat, oldMeta := formatFlag, clientMeta
			t.Cleanup(func() { formatFlag, clientMeta = oldFormat, oldMeta })
			formatFlag = "json"
			command := &cobra.Command{}
			if err := rootCmd.PersistentPreRunE(command, nil); err != nil {
				t.Fatal(err)
			}
			text, err := captureHeartbeatStdout(t, func() error { return heartbeatPlanCmd.RunE(command, nil) })
			if err != nil {
				t.Fatal(err)
			}
			var plan heartbeatPlan
			if err := json.Unmarshal([]byte(text), &plan); err != nil {
				t.Fatal(err)
			}
			home, _ := config.HomeDirInfo()
			for _, part := range []string{"--homedir " + shellQuote(home), "--server " + shellQuote(serverName), "--runtime-mode " + shellQuote(mode)} {
				if !strings.Contains(plan.CLIPrefix, part) || !strings.Contains(plan.SchedulerLauncher, part) {
					t.Fatalf("legacy identity lost %q: %+v", part, plan)
				}
			}
			if !strings.Contains(plan.SchedulerMigration, "Reuse working existing triggers") || !strings.Contains(plan.AgentPrompt, "including legacy EIGENFLUX_MODE launchers") {
				t.Fatal("missing existing-user compatibility guidance")
			}
		})
	}
}
