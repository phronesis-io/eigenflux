package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/maintenance"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/spf13/cobra"
)

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
