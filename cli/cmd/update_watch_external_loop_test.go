package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/maintenance"
	watchstate "cli.eigenflux.ai/internal/watch"
	"cli.eigenflux.ai/internal/watchservice"
	"github.com/spf13/cobra"
)

func TestLinuxRecoveryPreservesOrdinaryWatch(t *testing.T) {
	api := outerLoopTestAPI(t)
	cfg, _ := runtimeTestConfig(t, api.URL, true)
	installHeartbeatTestRules(t)
	clientMeta.Host, clientMeta.Mode = "codex", "skill"
	t.Setenv("EIGENFLUX_SKIP_AGENT_SETUP", "")
	expected, err := watchBindingIdentity()
	if err != nil {
		t.Fatal(err)
	}
	live := watchLiveStatus{State: "ready", Running: true, Ready: true, Commit: commit,
		Home: expected.Home, Server: expected.Server, Endpoint: expected.Endpoint,
		Scope: expected.Scope, UpdatedAt: time.Now()}
	if err := dispatch.WriteJSON(watchLivePath(expected), live); err != nil {
		t.Fatal(err)
	}
	release, err := watchstate.Acquire(expected.Home, expected.Server)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	before, err := os.ReadFile(watchLivePath(expected))
	if err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd.SetContext(ctx)
	cmd.SetErr(&bytes.Buffer{})
	r := recoverWatchAfterUpdateOnPlatform(cmd, cfg, false, "linux")
	if r.Status != "ready" || r.Mode != "watch" || !r.RuntimeReady || !r.VersionVerified || !r.SkillsVerified || r.AgentCallVerified || r.MainSession != nil {
		t.Fatalf("ordinary watch was not verified in its existing mode: %+v", r)
	}
	after, err := os.ReadFile(watchLivePath(expected))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("recovery rewrote the ordinary watch pulse: %v", err)
	}
	for _, path := range []string{dispatch.BindingPath(expected.Home, expected.Server), notificationWatchPath(expected.Home, expected.Server)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("ordinary watch recovery created another ownership mode at %s: %v", path, err)
		}
	}
}

func TestLinuxRecoveryRejectsInvalidOrdinaryWatch(t *testing.T) {
	for _, scenario := range []string{"missing", "stale", "future", "account_changed", "configuration_changed", "revision_changed", "notifications_only", "unlocked", "stopped"} {
		t.Run(scenario, func(t *testing.T) {
			api := outerLoopTestAPI(t)
			cfg, server := runtimeTestConfig(t, api.URL, true)
			t.Setenv("EIGENFLUX_SKIP_AGENT_SETUP", "")
			expected, err := watchBindingIdentity()
			if err != nil {
				t.Fatal(err)
			}
			live := watchLiveStatus{State: "ready", Running: true, Ready: true, Commit: commit,
				Home: expected.Home, Server: expected.Server, Endpoint: expected.Endpoint,
				Scope: expected.Scope, UpdatedAt: time.Now()}
			switch scenario {
			case "stale":
				live.UpdatedAt = time.Now().Add(-time.Minute)
			case "future":
				live.UpdatedAt = time.Now().Add(time.Minute)
			case "account_changed":
				credentials, err := auth.LoadV2Credentials(server)
				if err != nil {
					t.Fatal(err)
				}
				credentials.AgentID = "another-account"
				if err := auth.SaveV2Credentials(server, credentials); err != nil {
					t.Fatal(err)
				}
			case "configuration_changed":
				if err := cfg.UpdateServer(server, "https://changed.example.invalid", ""); err != nil {
					t.Fatal(err)
				}
			case "revision_changed":
				live.Revision = "new-binding"
			case "notifications_only":
				live.NotificationsOnly = true
			case "stopped":
				live.Running = false
			}
			if scenario != "missing" {
				if err := dispatch.WriteJSON(watchLivePath(expected), live); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "unlocked" {
				release, err := watchstate.Acquire(expected.Home, expected.Server)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			before, _ := os.ReadFile(watchLivePath(expected))
			cmd := &cobra.Command{}
			cmd.SetErr(&bytes.Buffer{})
			r := recoverWatchAfterUpdateOnPlatform(cmd, cfg, false, "linux")
			if r.Status != "pending" || r.Reason != "external_loop_unverified" || r.RuntimeReady || r.VersionVerified || r.SkillsVerified {
				t.Fatalf("invalid ordinary watch was accepted: %+v", r)
			}
			after, _ := os.ReadFile(watchLivePath(expected))
			if !bytes.Equal(before, after) {
				t.Fatal("recovery changed the existing pulse")
			}
			for _, path := range []string{dispatch.BindingPath(expected.Home, server), notificationWatchPath(expected.Home, server)} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("recovery created another owner at %s: %v", path, err)
				}
			}
		})
	}
}

func TestOrdinaryWatchRecoveryDoesNotStartOrPromoteItsOwner(t *testing.T) {
	for _, scenario := range []string{"ready", "stopped_before_checks", "stopped_during_checks", "identity_changed", "configuration_changed", "old_version"} {
		t.Run(scenario, func(t *testing.T) {
			expected := dispatch.Binding{Home: t.TempDir(), Server: "test", Endpoint: "https://test.invalid", AgentID: "agent", Scope: "scope", Host: "codex"}
			var reads, checks, starts, probes int
			deps := updateRecoveryDeps{ordinaryWatch: true,
				verify: func() error {
					checks++
					if checks > 1 {
						if scenario == "identity_changed" {
							return errWatchIdentity
						}
						if scenario == "configuration_changed" {
							return errWatchConfiguration
						}
					}
					return nil
				},
				sync: func(dispatch.Binding) error { return nil },
				start: func(context.Context, watchservice.NotificationTask) (watchservice.Result, error) {
					starts++
					return watchservice.Result{}, errors.New("must preserve the existing supervisor")
				},
				probe: func(context.Context, dispatch.Binding) dispatch.MainNoticeProbe {
					probes++
					return dispatch.MainNoticeProbe{}
				},
				live: func(_ dispatch.Binding, notificationsOnly bool) watchLiveStatus {
					reads++
					if notificationsOnly {
						t.Fatal("ordinary watch was read as notifications-only")
					}
					if scenario == "stopped_before_checks" || scenario == "stopped_during_checks" && reads > 1 {
						return watchLiveStatus{}
					}
					runningCommit := commit
					if scenario == "old_version" {
						runningCommit = commit + "-old"
					}
					return watchLiveStatus{Running: true, Ready: true, Commit: runningCommit, AgentCallVerified: true}
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			defer cancel()
			r := recoverUpdatedWatch(ctx, expected, nil, "/unused-cli", deps)
			if starts != 0 || probes != 0 || r.Mode != "watch" || r.AgentCallVerified || r.MainSession != nil {
				t.Fatalf("ordinary watch was started or promoted to Agent dispatch: starts=%d probes=%d result=%+v", starts, probes, r)
			}
			if scenario == "ready" {
				if r.Status != "ready" || !r.RuntimeReady || !r.VersionVerified {
					t.Fatalf("existing ordinary owner was not accepted: %+v", r)
				}
			} else if r.Status == "ready" {
				t.Fatalf("changed or unavailable owner counted as ready: %+v", r)
			}
		})
	}
}

func TestLinuxRecoveryDoesNotTreatPluginModeAsReadiness(t *testing.T) {
	api := outerLoopTestAPI(t)
	cfg, server := runtimeTestConfig(t, api.URL, true)
	clientMeta.Host, clientMeta.Mode = "openclaw", "plugin"
	t.Setenv("EIGENFLUX_SKIP_AGENT_SETUP", "")
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	r := recoverWatchAfterUpdateOnPlatform(cmd, cfg, true, "linux")
	if r.Status != "pending" || r.Reason != "host_managed_unverified" || r.Mode != "plugin" || r.RuntimeReady || r.AgentCallVerified || watchRecoveryError(r) == nil {
		t.Fatalf("plugin intent is not a runtime receipt: %+v", r)
	}
	if !strings.Contains(r.Action, "do not add a second scheduler") {
		t.Fatalf("missing host-owned recovery action: %+v", r)
	}
	expected, err := watchBindingIdentity()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dispatch.BindingPath(expected.Home, server), notificationWatchPath(expected.Home, server), watchLivePath(expected)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("Linux inspection created or changed ownership at %s: %v", path, err)
		}
	}
	scope, _ := maintenanceScope(server)
	state, err := maintenance.Snapshot(scope)
	if err != nil || len(state.Events) < 2 {
		t.Fatalf("missing recovery observation: %+v %v", state, err)
	}
	last := state.Events[len(state.Events)-1]
	if last.Result != "skipped" || last.ErrorCode != "host_managed" {
		t.Fatalf("plugin ownership was misreported as unsupported or ready: %+v", last)
	}
}

func TestLinuxRecoveryRequiresMatchingLiveOwner(t *testing.T) {
	b := dispatch.Binding{Home: t.TempDir(), Server: "test", Endpoint: "https://test.invalid", Scope: "identity", Revision: "revision"}
	now := time.Now()
	s := watchLiveStatus{State: "ready", Running: true, Ready: true, Home: b.Home, Server: b.Server, Endpoint: b.Endpoint, Scope: b.Scope, Revision: b.Revision, UpdatedAt: now}
	if err := dispatch.WriteJSON(watchLivePath(b), s); err != nil {
		t.Fatal(err)
	}
	check := func(proceed bool) {
		t.Helper()
		r, got := linuxWatchRecoveryStatus("skill", readWatchLiveStatus(b, false, now))
		if got != proceed {
			t.Fatalf("proceed=%t, want %t: %+v", got, proceed, r)
		}
		if !got && (r.Status != "pending" || r.Reason != "external_loop_unverified" || r.RuntimeReady || r.Action == "" || watchRecoveryError(r) == nil) {
			t.Fatalf("missing actionable unverified state: %+v", r)
		}
	}
	check(false) // A saved pulse without its live account lock is historical.
	release, err := watchstate.Acquire(b.Home, b.Server)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	check(true)
	for _, change := range []func(){
		func() { s.UpdatedAt = now.Add(-time.Minute) },
		func() { s.Scope = "other-account" },
		func() { s.Revision = "other-binding" },
	} {
		original := s
		change()
		if err := dispatch.WriteJSON(watchLivePath(b), s); err != nil {
			t.Fatal(err)
		}
		check(false)
		s = original
	}
}

func TestLinuxRecoveryPreservesNotificationOwnership(t *testing.T) {
	for _, scenario := range []string{"missing", "foreign", "workbuddy"} {
		t.Run(scenario, func(t *testing.T) {
			api := outerLoopTestAPI(t)
			cfg, _ := runtimeTestConfig(t, api.URL, true)
			clientMeta.Host, clientMeta.Mode = "workbuddy", "skill"
			t.Setenv("EIGENFLUX_SKIP_AGENT_SETUP", "")
			t.Setenv("EIGENFLUX_SKILLS_DIR", t.TempDir())
			expected, err := watchBindingIdentity()
			if err != nil {
				t.Fatal(err)
			}
			owner := expected
			owner.Version = 1
			if scenario == "foreign" {
				owner.AgentID = "another-account"
			}
			path := notificationWatchPath(expected.Home, expected.Server)
			if scenario != "missing" {
				if err := dispatch.WriteJSON(path, owner); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			live := watchLiveStatus{State: "ready", Running: true, Ready: true, NotificationsOnly: true,
				Home: expected.Home, Server: expected.Server, Endpoint: expected.Endpoint, Scope: expected.Scope, UpdatedAt: time.Now()}
			if err := dispatch.WriteJSON(watchLivePath(expected), live); err != nil {
				t.Fatal(err)
			}
			release, err := watchstate.Acquire(expected.Home, expected.Server)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			cmd := &cobra.Command{}
			cmd.SetErr(&bytes.Buffer{})
			r := recoverWatchAfterUpdateOnPlatform(cmd, cfg, false, "linux")
			want := map[string]string{"missing": "external_loop_unverified", "foreign": "identity_changed", "workbuddy": "skills_local_validation_failed"}[scenario]
			if r.Reason != want || r.RuntimeReady {
				t.Fatalf("Linux recovery accepted orphan ownership or entered host setup: %+v", r)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("Linux recovery replaced the existing notification owner")
			}
			if _, err := os.Stat(dispatch.BindingPath(expected.Home, expected.Server)); !os.IsNotExist(err) {
				t.Fatalf("Linux recovery created a dispatch binding: %v", err)
			}
		})
	}
}

func TestWatchRecoveryExitKeepsIncompleteStatesDistinct(t *testing.T) {
	for _, status := range []string{"pending", "starting", "blocked"} {
		r := updateWatchRecovery{Status: status, Reason: "external_loop_unverified", Action: "verify the existing scheduler"}
		if err := watchRecoveryError(r); err == nil || !strings.Contains(err.Error(), r.Action) {
			t.Fatalf("incomplete recovery returned success or lost its action: %+v %v", r, err)
		}
	}
	for _, status := range []string{"ready", "skipped"} {
		if err := watchRecoveryError(updateWatchRecovery{Status: status}); err != nil {
			t.Fatalf("ready or owner-disabled recovery failed: %v", err)
		}
	}
}
