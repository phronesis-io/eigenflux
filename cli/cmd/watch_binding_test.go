package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/spf13/cobra"
)

func bindingTestCommand() (*cobra.Command, *bytes.Buffer) {
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	return cmd, out
}

func TestWatchBindingPinsCurrentIdentityAndRebindsAcceptedJobs(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	w := f.watch
	original := *w.binding
	original.Events = []string{"pm_push", "maintenance_due"}
	if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), original); err != nil {
		t.Fatal(err)
	}
	journal, err := dispatch.OpenJournal(original)
	if err != nil {
		t.Fatal(err)
	}
	if err = journal.AddHint("maintenance_due", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	job, ok, err := journal.Next()
	if err != nil || !ok {
		t.Fatalf("next hint: %v %v", ok, err)
	}
	if err = journal.Update(job.ID, "accepted", "business_unconfirmed", "", ""); err != nil {
		t.Fatal(err)
	}

	submitted := original
	foreignHome := t.TempDir()
	submitted.Home = foreignHome
	submitted.Server = "foreign-server"
	submitted.AgentID = "foreign-agent"
	submitted.PrincipalID = "foreign-principal"
	submitted.Endpoint = "https://foreign.example.test"
	submitted.Scope = "foreign-scope"
	submitted.Revision = "untrusted-input-revision"
	configFile := filepath.Join(t.TempDir(), "binding.json")
	if err = dispatch.WriteJSON(configFile, submitted); err != nil {
		t.Fatal(err)
	}
	command, out := bindingTestCommand()
	command.Flags().String("config", configFile, "")
	if err = watchBindCmd.RunE(command, nil); err != nil {
		t.Fatalf("accepted terminal job blocked rebind: %v", err)
	}
	rebound, err := dispatch.ReadBinding(w.home, w.server.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !sameBindingIdentity(rebound, original) || rebound.Revision == original.Revision || rebound.Revision == submitted.Revision {
		t.Fatalf("bind did not pin current identity and renew revision: %+v", rebound)
	}
	if !strings.Contains(out.String(), `"status":"bound"`) || strings.Contains(out.String(), "foreign-agent") {
		t.Fatalf("wrong binding response: %s", out)
	}
	if _, err = os.Stat(dispatch.BindingPath(foreignHome, submitted.Server)); !os.IsNotExist(err) {
		t.Fatalf("bind wrote foreign Home: %v", err)
	}
	oldJobs, err := dispatch.ReadJournalStatus(original)
	if err != nil || len(oldJobs) != 1 || oldJobs[0].Status != "accepted" {
		t.Fatalf("rebind lost old journal: %+v %v", oldJobs, err)
	}
}

type bindingNoNetworkTransport struct{ calls *int }

func (t bindingNoNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	*t.calls++
	return nil, errors.New("doctor must not use network")
}

func TestWatchBindingDoctorExecutionSentinel(t *testing.T) {
	marker := os.Getenv("EF_BINDING_DOCTOR_EXECUTION_MARKER")
	if marker == "" {
		return
	}
	if err := os.WriteFile(marker, []byte("agent executed"), 0600); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestWatchBindingDoctorIsStatic(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	b := *f.watch.binding
	marker := filepath.Join(t.TempDir(), "execution-marker")
	b.Command = append(b.Command, "-test.run=^TestWatchBindingDoctorExecutionSentinel$")
	b.Env = map[string]string{"EF_BINDING_DOCTOR_EXECUTION_MARKER": marker}
	if err := dispatch.WriteJSON(dispatch.BindingPath(b.Home, b.Server), b); err != nil {
		t.Fatal(err)
	}
	calls := 0
	previousTransport := http.DefaultTransport
	http.DefaultTransport = bindingNoNetworkTransport{calls: &calls}
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	cmd, out := bindingTestCommand()
	if err := watchDoctorCmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status           string `json:"status"`
		ProtocolChecked  bool   `json:"protocol_checked"`
		BusinessVerified bool   `json:"business_verified"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "configuration_ready" || result.ProtocolChecked || result.BusinessVerified || calls != 0 {
		t.Fatalf("doctor executed work or overstated verification: %+v network=%d", result, calls)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("doctor invoked Agent: %v", err)
	}
}

func TestWatchBindingStatusDoesNotRecoverRunningJobs(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	job := dispatchTestNext(t, f.watch)
	cmd, out := bindingTestCommand()
	if err := watchStatusCmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status string         `json:"status"`
		Jobs   []dispatch.Job `json:"jobs"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "bound" || len(result.Jobs) != 1 || result.Jobs[0].ID != job.ID || result.Jobs[0].Status != "running" {
		t.Fatalf("status changed execution: %s", out)
	}
	if result.Jobs[0].Message == nil || result.Jobs[0].Message.Content != "" || result.Jobs[0].Data != nil || strings.Contains(out.String(), "Please answer") {
		t.Fatal("status exposed message body")
	}
	persisted, err := dispatch.ReadJournalStatus(*f.watch.binding)
	if err != nil || len(persisted) != 1 || persisted[0].Status != "running" {
		t.Fatalf("status recovered persistent journal: %+v %v", persisted, err)
	}
}

func TestWatchBindingRetryHonorsOwnerLockAndUnknownState(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	w := f.watch
	job := dispatchTestNext(t, w)
	if err := w.journal.Update(job.ID, "unknown", "reply_unconfirmed", "", ""); err != nil {
		t.Fatal(err)
	}
	release, err := watchstate.Acquire(w.home, w.server.Name)
	if err != nil {
		t.Fatal(err)
	}
	cmd, _ := bindingTestCommand()
	lockedErr := watchRetryCmd.RunE(cmd, []string{job.ID})
	release()
	if lockedErr == nil || !strings.Contains(lockedErr.Error(), "watch already active") {
		t.Fatalf("retry bypassed watch owner: %v", lockedErr)
	}
	cmd, _ = bindingTestCommand()
	if err = watchRetryCmd.RunE(cmd, []string{job.ID}); err == nil || !strings.Contains(err.Error(), "job_not_safe_to_retry") {
		t.Fatalf("unknown job retried: %v", err)
	}
	persisted, err := dispatch.ReadJournalStatus(*w.binding)
	if err != nil || len(persisted) != 1 || persisted[0].Status != "unknown" {
		t.Fatalf("retry altered unknown job: %+v %v", persisted, err)
	}
}

func TestWatchBindingOwnershipRemovesOnlyEnabledStages(t *testing.T) {
	cases := []struct {
		name              string
		events, remaining []string
	}{
		{"PM only", []string{"pm_push"}, []string{"commands", "feed", "attention", "publish", "settings_report"}},
		{"control only", []string{"control_pending"}, []string{"feed", "attention", "communication", "publish", "settings_report"}},
		{"both", []string{"pm_push", "control_pending"}, []string{"feed", "attention", "publish", "settings_report"}},
		{"maintenance only", []string{"maintenance_due"}, []string{"commands", "feed", "attention", "communication", "publish", "settings_report"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDispatchWatchFixture(t, "")
			b := *f.watch.binding
			b.Events = tc.events
			if err := dispatch.WriteJSON(dispatch.BindingPath(b.Home, b.Server), b); err != nil {
				t.Fatal(err)
			}
			plan := heartbeatPlan{ExecutionOrder: []string{"commands", "feed", "attention", "communication", "publish", "settings_report"}}
			if err := applyDispatchOwnership(&plan); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(plan.ExecutionOrder, tc.remaining) || !reflect.DeepEqual(plan.DispatchOwned, tc.events) {
				t.Fatalf("ownership removed unrelated stages: %+v", plan)
			}
		})
	}
	t.Run("changed identity", func(t *testing.T) {
		f := newDispatchWatchFixture(t, "")
		replacement := f.watch.identity
		replacement.AgentID = "other-agent"
		replacement.PrincipalID = "other-principal"
		if err := auth.SaveV2Credentials(f.watch.server.Name, &replacement); err != nil {
			t.Fatal(err)
		}
		plan := heartbeatPlan{ExecutionOrder: []string{"feed", "communication"}}
		before := append([]string{}, plan.ExecutionOrder...)
		if err := applyDispatchOwnership(&plan); !errors.Is(err, errWatchIdentity) {
			t.Fatalf("mismatched owner not rejected: %v", err)
		}
		if !reflect.DeepEqual(plan.ExecutionOrder, before) {
			t.Fatal("failed ownership check mutated plan")
		}
	})
}

func TestWatchBindingReconcileOptionalEvent(t *testing.T) {
	for _, source := range []string{"running", "failed", "needs_user"} {
		for _, outcome := range []string{"completed", "failed"} {
			t.Run(source+"/"+outcome, func(t *testing.T) {
				f := newDispatchWatchFixture(t, "")
				b := *f.watch.binding
				b.Events = append(b.Events, "maintenance_due")
				if err := dispatch.WriteJSON(dispatch.BindingPath(b.Home, b.Server), b); err != nil {
					t.Fatal(err)
				}
				q, err := dispatch.OpenJournal(b)
				if err != nil {
					t.Fatal(err)
				}
				if err = q.AddHint("maintenance_due", json.RawMessage(`{}`)); err != nil {
					t.Fatal(err)
				}
				job, ok, err := q.Next()
				if err != nil || !ok {
					t.Fatalf("next: %v %v", ok, err)
				}
				if source != "running" {
					if err = q.Update(job.ID, source, "business_unconfirmed", "", ""); err != nil {
						t.Fatal(err)
					}
				}
				cmd, out := bindingTestCommand()
				cmd.Flags().Bool("verified", false, "")
				cmd.Flags().String("outcome", outcome, "")
				cmd.Flags().String("reply-id", "", "")
				if err = watchReconcileCmd.RunE(cmd, []string{job.ID}); err == nil {
					t.Fatal("reconciliation did not require verification")
				}
				if err = cmd.Flags().Set("verified", "true"); err != nil {
					t.Fatal(err)
				}
				if err = watchReconcileCmd.RunE(cmd, []string{job.ID}); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), `"status":"`+outcome+`"`) {
					t.Fatalf("wrong reconciliation: %s", out)
				}
				q, err = dispatch.OpenJournal(b)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok, err = q.Next(); ok || err != nil {
					t.Fatalf("reconciliation scheduled automatic retry: %v %v", ok, err)
				}
			})
		}
	}
}

func TestWatchBindingReconcilesManuallyHandledPMBeforeRebind(t *testing.T) {
	for _, source := range []string{"failed", "needs_user"} {
		for _, outcome := range []string{"replied", "no_reply"} {
			t.Run(source+"/"+outcome, func(t *testing.T) {
				f := newDispatchWatchFixture(t, "")
				w := f.watch
				job := dispatchTestNext(t, w)
				if err := w.journal.Update(job.ID, source, "manual_review", "", ""); err != nil {
					t.Fatal(err)
				}
				cmd, _ := bindingTestCommand()
				cmd.Flags().Bool("verified", false, "")
				cmd.Flags().String("outcome", outcome, "")
				replyID := ""
				if outcome == "replied" {
					replyID = "human-reply-receipt"
				}
				cmd.Flags().String("reply-id", replyID, "")
				if err := watchReconcileCmd.RunE(cmd, []string{job.ID}); err == nil {
					t.Fatal("unverified manual resolution accepted")
				}
				if err := cmd.Flags().Set("verified", "true"); err != nil {
					t.Fatal(err)
				}
				if err := watchReconcileCmd.RunE(cmd, []string{job.ID}); err != nil {
					t.Fatal(err)
				}
				q, err := dispatch.OpenJournal(*w.binding)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok, err := q.Next(); ok || err != nil {
					t.Fatalf("manual resolution reran Agent: %v %v", ok, err)
				}
				jobs := q.Snapshot()
				if len(jobs) != 1 || jobs[0].Status != outcome || jobs[0].ReplyID != replyID {
					t.Fatalf("manual outcome was not persisted: %+v", jobs)
				}
				configFile := filepath.Join(t.TempDir(), "binding.json")
				if err := dispatch.WriteJSON(configFile, *w.binding); err != nil {
					t.Fatal(err)
				}
				cmd, _ = bindingTestCommand()
				cmd.Flags().String("config", configFile, "")
				if err := watchBindCmd.RunE(cmd, nil); err != nil {
					t.Fatalf("resolved PM blocked configuration repair: %v", err)
				}
			})
		}
	}
}
