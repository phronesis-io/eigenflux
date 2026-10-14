package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/spf13/cobra"
)

func upgradeHeartbeatFixture(t *testing.T, baseline bool) (dispatch.Binding, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/agent-context" {
			if baseline {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":{"code":"ONBOARDING_REQUIRED","details":{"onboarding_state":"in_progress"}}}`))
			} else {
				_, _ = w.Write([]byte(`{"code":0,"data":{"context_revision":1}}`))
			}
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	t.Cleanup(server.Close)
	cfg, name := runtimeTestConfig(t, server.URL, true)
	if err := cfg.SetKV("auto_cli_update", "false"); err != nil {
		t.Fatal(err)
	}
	clientMeta.Host, clientMeta.Mode = "codex", "skill"
	rules := installHeartbeatTestRules(t)
	version = "1.0.10"
	cred, err := auth.LoadV2Credentials(name)
	if err != nil {
		t.Fatal(err)
	}
	cred.PrincipalID = "principal-1"
	if err := auth.SaveV2Credentials(name, cred); err != nil {
		t.Fatal(err)
	}
	b, err := watchBindingIdentity()
	if err != nil {
		t.Fatal(err)
	}
	b = lifecycleCodexBinding(t, b)
	b.Command = []string{os.Args[0]}
	b.WorkDir, b.SkillsDir, b.Revision = t.TempDir(), rules, strings.Repeat("a", 32)
	b.Events = []string{"pm_push"}
	oldFormat := formatFlag
	formatFlag = "json"
	t.Cleanup(func() { formatFlag = oldFormat })
	return b, rules
}

func runUpgradeHeartbeat(t *testing.T, cmd *cobra.Command) heartbeatPlan {
	t.Helper()
	raw, err := captureHeartbeatStdout(t, func() error { return heartbeatPlanCmd.RunE(cmd, nil) })
	if err != nil {
		t.Fatal(err)
	}
	var plan heartbeatPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestHeartbeatUpgradeAutomaticallySchedulesInitialBinding(t *testing.T) {
	for _, state := range []string{"upgrade", "watch-managed", "watch-owns-maintenance", "ready", "unavailable", "maintenance", "control", "baseline", "other-host", "plugin", "unsigned-rules"} {
		t.Run(state, func(t *testing.T) {
			b, rules := upgradeHeartbeatFixture(t, state == "baseline")
			cmd := &cobra.Command{}
			switch state {
			case "watch-managed", "watch-owns-maintenance":
				cmd.Flags().Bool("watch-managed", true, "")
				if state == "watch-owns-maintenance" {
					b.Events = append(b.Events, "maintenance_due", "control_pending")
				}
			case "ready":
				b.MainSessionID = lifecycleThread
			case "unavailable":
				b.MainSessionID = "01a0d200-9d41-7cd2-9214-3f04761910c5"
			case "maintenance", "control":
				cmd.Flags().Bool(state+"-only", true, "")
			case "other-host":
				clientMeta.Host = "terminal"
			case "plugin":
				clientMeta.Mode = "plugin"
			}
			path := dispatch.BindingPath(b.Home, b.Server)
			if err := dispatch.WriteJSON(path, b); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if state == "unsigned-rules" {
				if err := os.WriteFile(filepath.Join(rules, "ef-communication", "references", "main-session.md"), []byte("Unsigned replacement"), 0600); err != nil {
					t.Fatal(err)
				}
				raw, err := captureHeartbeatStdout(t, func() error { return heartbeatPlanCmd.RunE(cmd, nil) })
				if err == nil {
					var plan heartbeatPlan
					if json.Unmarshal([]byte(raw), &plan) != nil || slices.Contains(plan.ExecutionOrder, "main_session_bootstrap") || plan.MainSessionBinding.Error == "" {
						t.Fatal("unsigned binding instructions were admitted")
					}
				}
				after, _ := os.ReadFile(path)
				if string(before) != string(after) {
					t.Fatal("unsigned rules changed binding")
				}
				return
			}
			plan := runUpgradeHeartbeat(t, cmd)
			want := state == "upgrade" || state == "watch-managed" || state == "watch-owns-maintenance"
			if slices.Contains(plan.ExecutionOrder, "main_session_bootstrap") != want {
				t.Fatalf("automatic bootstrap=%v: %+v", want, plan)
			}
			if want {
				if plan.ExecutionOrder[0] != "main_session_bootstrap" || !plan.WakeOnEmpty ||
					!slices.Contains(plan.RuleSources, filepath.Join(rules, "ef-communication", "references", "main-session.md")) ||
					!strings.Contains(plan.AgentPrompt, "Execute main_session_bootstrap automatically before business stages") {
					t.Fatalf("upgrade did not execute signed bootstrap before business work: %+v", plan)
				}
			} else if strings.Contains(plan.AgentPrompt, "Execute main_session_bootstrap automatically") {
				t.Fatal("narrow, unsupported or already bound execution requested bootstrap")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("planning bypassed the guarded host-owned binding flow")
			}
		})
	}
}

func TestHeartbeatUpgradeBindingCompletesOnceAndPreservesJournal(t *testing.T) {
	b, _ := upgradeHeartbeatFixture(t, false)
	path := dispatch.BindingPath(b.Home, b.Server)
	if err := dispatch.WriteJSON(path, b); err != nil {
		t.Fatal(err)
	}
	journal, err := dispatch.OpenJournal(b)
	if err != nil {
		t.Fatal(err)
	}
	batch, _ := json.Marshal(map[string]any{"messages": []dispatch.Message{{ID: "old-message", Conversation: "old-conversation", Sender: "peer", Receiver: b.AgentID}}})
	if err := journal.AddMessages(batch); err != nil {
		t.Fatal(err)
	}
	first := runUpgradeHeartbeat(t, &cobra.Command{})
	if first.ExecutionOrder[0] != "main_session_bootstrap" {
		t.Fatal("upgrade omitted automatic bootstrap")
	}
	cmd, _ := bindingTestCommand()
	addLifecycleSchedulerFlags(cmd)
	raw, _ := json.Marshal(lifecycleInventory(b))
	cmd.SetIn(strings.NewReader(string(raw)))
	_ = cmd.Flags().Set("scheduler-stdin", "true")
	_ = cmd.Flags().Set("preflight", "true")
	_ = cmd.Flags().Set("expected-id", "")
	_ = cmd.Flags().Set("expected-revision", b.Revision)
	release, err := watchstate.Acquire(b.Home, b.Server)
	if err != nil {
		t.Fatal(err)
	}
	err = updateWatchMainSession(cmd)
	release()
	if err != nil {
		t.Fatal(err)
	}
	cmd.SetIn(strings.NewReader(string(raw)))
	_ = cmd.Flags().Set("preflight", "false")
	_ = cmd.Flags().Set("expected-task-id", "host-owned-task")
	_ = cmd.Flags().Set("expected-thread-id", lifecycleThread)
	changed := lifecycleInventory(b)
	changed.Tasks[0].ThreadID = "01a0d200-9d41-7cd2-9214-3f04761910c5"
	changedRaw, _ := json.Marshal(changed)
	cmd.SetIn(strings.NewReader(string(changedRaw)))
	if err := updateWatchMainSession(cmd); err == nil {
		t.Fatal("upgrade committed a changed scheduler target")
	}
	retry := runUpgradeHeartbeat(t, &cobra.Command{})
	if retry.ExecutionOrder[0] != "main_session_bootstrap" {
		t.Fatal("failed migration was not retried by the next full heartbeat")
	}
	cmd.SetIn(strings.NewReader(string(raw)))
	if err := updateWatchMainSession(cmd); err != nil {
		t.Fatal(err)
	}
	second := runUpgradeHeartbeat(t, &cobra.Command{})
	if second.MainSessionBinding.Status != "ready" || slices.Contains(second.ExecutionOrder, "main_session_bootstrap") {
		t.Fatal("completed upgrade kept requesting initial binding")
	}
	got, err := dispatch.ReadBinding(b.Home, b.Server)
	if err != nil || got.Revision != b.Revision || got.MainSessionID != lifecycleThread {
		t.Fatalf("upgrade changed binding identity/revision: %+v %v", got, err)
	}
	jobs, err := dispatch.ReadJournalStatus(got)
	if err != nil || len(jobs) != 1 || jobs[0].Message.ID != "old-message" {
		t.Fatalf("upgrade lost pending private messages: %+v %v", jobs, err)
	}
}
