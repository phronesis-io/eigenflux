package cmd

import (
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/watchservice"
	"context"
	"errors"
	"testing"
	"time"
)

func TestUpgradeWatchRecovery(t *testing.T) {
	for _, scenario := range []string{"bound", "notifications", "wrong_account", "skills_failed", "disabled", "running", "start_failed", "not_ready", "identity_switch", "owner_locked"} {
		t.Run(scenario, func(t *testing.T) {
			expected := dispatch.Binding{Home: "/home/agent", Server: "eigenflux", Endpoint: "https://example.com", AgentID: "42", PrincipalID: "7", Scope: "scope"}
			b := expected
			b.Host = "codex"
			b.Revision = "revision"
			b.SkillsDir = "/rules"
			binding := &b
			if scenario == "notifications" {
				binding = nil
			}
			if scenario == "wrong_account" {
				b.AgentID = "43"
			}
			var syncs, starts, checks, reads int
			deps := updateRecoveryDeps{
				idle: func() error {
					if scenario == "owner_locked" {
						return errors.New("busy")
					}
					return nil
				},
				verify: func() error {
					checks++
					if scenario == "identity_switch" && checks > 1 {
						return errors.New("changed")
					}
					return nil
				},
				sync: func(got dispatch.Binding) error {
					syncs++
					if binding != nil && got.SkillsDir != "/rules" {
						t.Fatal("lost binding rules")
					}
					if scenario == "skills_failed" {
						return errors.New("untrusted")
					}
					return nil
				},
				start: func(_ context.Context, task watchservice.NotificationTask) (watchservice.Result, error) {
					starts++
					if task.Home != expected.Home || task.AgentID != "42" || task.Dispatch != (binding != nil) {
						t.Fatal("wrong account/mode")
					}
					if scenario == "start_failed" {
						return watchservice.Result{}, errors.New("failed")
					}
					status := "process_started"
					if scenario == "disabled" {
						status = "skipped_disabled"
					}
					return watchservice.Result{Status: status}, nil
				},
				live: func(got dispatch.Binding, notifications bool) watchLiveStatus {
					reads++
					if notifications != (binding == nil) {
						t.Fatal("wrong live scope")
					}
					if scenario == "running" {
						return watchLiveStatus{Running: true, Ready: true}
					}
					return watchLiveStatus{Running: reads > 1, Ready: reads > 1 && scenario != "not_ready"}
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			defer cancel()
			got := recoverUpdatedWatch(ctx, expected, binding, "/bin/eigenflux", deps)
			want := map[string]string{"bound": "ready", "notifications": "ready", "wrong_account": "blocked", "skills_failed": "blocked", "disabled": "skipped", "running": "running", "start_failed": "blocked", "not_ready": "starting", "identity_switch": "blocked", "owner_locked": "blocked"}[scenario]
			if got.Status != want {
				t.Fatalf("%+v", got)
			}
			if (scenario == "wrong_account" || scenario == "owner_locked") && (starts != 0 || syncs != 0) {
				t.Fatal("foreign identity mutated")
			}
			if (scenario == "skills_failed" || scenario == "identity_switch") && starts != 0 {
				t.Fatal("started despite failed preflight")
			}
			if scenario == "running" && (starts != 0 || syncs != 0) {
				t.Fatal("interrupted running work")
			}
			if scenario == "not_ready" && got.RuntimeReady {
				t.Fatal("startup is not readiness")
			}
		})
	}
}
