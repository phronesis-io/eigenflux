package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/skills"
	watchstate "cli.eigenflux.ai/internal/watch"
	"cli.eigenflux.ai/internal/watchservice"
	"github.com/spf13/cobra"
)

type updateWatchRecovery struct {
	Status            string `json:"status"`
	Mode              string `json:"mode,omitempty"`
	Reason            string `json:"reason,omitempty"`
	RuntimeReady      bool   `json:"runtime_ready"`
	AgentCallVerified bool   `json:"agent_call_verified"`
	DesktopStatus     string `json:"desktop_status,omitempty"`
}

type updateRecoveryDeps struct {
	idle   func() error
	verify func() error
	sync   func(dispatch.Binding) error
	start  func(context.Context, watchservice.NotificationTask) (watchservice.Result, error)
	live   func(dispatch.Binding, bool) watchLiveStatus
}

func recoverUpdatedWatch(ctx context.Context, expected dispatch.Binding, binding *dispatch.Binding, cli string, deps updateRecoveryDeps) updateWatchRecovery {
	r := updateWatchRecovery{Status: "blocked", Mode: "notifications_only"}
	if binding != nil {
		if !sameBindingIdentity(*binding, expected) {
			r.Reason = "identity_changed"
			return r
		}
		expected = *binding
		r.Mode = "dispatch"
	}
	if err := deps.verify(); err != nil {
		r.Reason = "identity_changed"
		return r
	}
	// A running owner must drain and adopt updates itself. Never interrupt paid work.
	if live := deps.live(expected, binding == nil); live.Running {
		r.Status = "running"
		r.RuntimeReady = live.Ready
		r.AgentCallVerified = live.AgentCallVerified
		r.DesktopStatus = live.DesktopStatus
		return r
	}
	if deps.idle != nil {
		if err := deps.idle(); err != nil {
			r.Reason = "owner_active_unverified"
			return r
		}
	}
	if err := deps.sync(expected); err != nil {
		r.Reason = "skills_not_ready"
		return r
	}
	if err := deps.verify(); err != nil {
		r.Reason = "identity_changed"
		return r
	}
	result, err := deps.start(ctx, watchservice.NotificationTask{CLI: cli, Home: expected.Home, Server: expected.Server, Scope: expected.Scope, AgentID: expected.AgentID, PrincipalID: expected.PrincipalID, Dispatch: binding != nil})
	if err != nil {
		r.Reason = "watch_start_failed"
		return r
	}
	if result.Status == "skipped_disabled" {
		r.Status = "skipped"
		r.Reason = "owner_disabled"
		return r
	}
	r.Status = "starting"
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := deps.verify(); err != nil {
			r.Status = "blocked"
			r.Reason = "identity_changed"
			return r
		}
		live := deps.live(expected, binding == nil)
		if live.Running && live.Ready {
			r.Status = "ready"
			r.RuntimeReady = true
			r.AgentCallVerified = live.AgentCallVerified
			r.DesktopStatus = live.DesktopStatus
			return r
		}
		select {
		case <-ctx.Done():
			r.Reason = "runtime_not_ready"
			return r
		case <-ticker.C:
		}
	}
}

func recoverWatchAfterUpdate(cmd *cobra.Command, cfg *config.Config, manual bool) updateWatchRecovery {
	r := updateWatchRecovery{Status: "skipped", Reason: "platform_not_supported"}
	if os.Getenv("EIGENFLUX_SKIP_AGENT_SETUP") == "1" {
		r.Reason = "owner_setup_disabled"
		return r
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		return r
	}
	if cfg == nil {
		r.Reason = "pending_auth"
		return r
	}
	expected, err := watchBindingIdentity()
	if err != nil {
		r.Status = "blocked"
		if errors.Is(err, os.ErrNotExist) {
			r.Status = "skipped"
			r.Reason = "pending_auth"
			return r
		}
		r.Reason = "identity_unavailable"
		return r
	}
	var binding *dispatch.Binding
	verify := func() error {
		current, err := watchBindingIdentity()
		if err != nil {
			return err
		}
		if !sameBindingIdentity(current, expected) {
			return errWatchIdentity
		}

		currentBinding, bindingErr := dispatch.ReadBinding(expected.Home, expected.Server)
		if binding != nil {
			if bindingErr != nil || !sameBindingIdentity(currentBinding, *binding) || currentBinding.Revision != binding.Revision {
				return errWatchConfiguration
			}
		} else {
			if !errors.Is(bindingErr, os.ErrNotExist) {
				return errWatchConfiguration
			}
			owner, ownerErr := readNotificationWatch(expected.Home, expected.Server)
			if ownerErr == nil && !sameBindingIdentity(owner, expected) {
				return errWatchIdentity
			}
			if ownerErr != nil && !errors.Is(ownerErr, os.ErrNotExist) {
				return ownerErr
			}
		}
		return nil
	}
	b, err := dispatch.ReadBinding(expected.Home, expected.Server)
	if err == nil {
		binding = &b
	} else if !errors.Is(err, os.ErrNotExist) {
		r.Status = "blocked"
		r.Reason = "binding_invalid"
		return r
	}
	// A host owns its existing binding. Do not discover or rebind another installed Agent.
	cli, err := stableInstalledExecutable()
	if err != nil {
		r.Status = "blocked"
		r.Reason = "installation_unavailable"
		return r
	}
	syncRules := func(b dispatch.Binding) error { return syncUpgradeWatchRules(cfg, b, manual) }

	if binding == nil {
		expected.Host = clientMetaForServerName(expected.Server).Host
		if expected.Host == "workbuddy" {
			if err := syncRules(expected); err != nil {
				r.Status = "blocked"
				r.Reason = "skills_not_ready"
				return r
			}
			var out bytes.Buffer
			setup := &cobra.Command{}
			setup.SetContext(cmd.Context())
			setup.SetOut(&out)
			setup.SetErr(cmd.ErrOrStderr())
			if err := startWorkBuddyWatch(setup); err != nil {
				r.Status = "blocked"
				r.Reason = "workbuddy_setup_failed"
				return r
			}
			var setupResult struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(out.Bytes(), &setupResult) != nil {
				r.Status = "blocked"
				r.Reason = "workbuddy_setup_failed"
				return r
			}
			if setupResult.Status == "skipped_disabled" {
				r.Status = "skipped"
				r.Reason = "owner_disabled"
				return r
			}
			b, err = dispatch.ReadBinding(expected.Home, expected.Server)
			if err != nil {
				r.Status = "blocked"
				r.Reason = "binding_invalid"
				return r
			}
			binding = &b
		}
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
	defer cancel()
	r = recoverUpdatedWatch(ctx, expected, binding, cli, updateRecoveryDeps{idle: func() error {
		release, err := watchstate.Acquire(expected.Home, expected.Server)
		if err == nil {
			release()
		}
		return err
	}, verify: verify, sync: syncRules, start: watchservice.StartNotifications, live: func(b dispatch.Binding, n bool) watchLiveStatus { return readWatchLiveStatus(b, n, time.Now()) }})
	return r
}

func syncUpgradeWatchRules(cfg *config.Config, b dispatch.Binding, manual bool) error {
	if manual || automaticMaintenanceEnabled(cfg, autoSkillSyncKey) {
		_, err := skills.Sync(skills.SyncOptions{Into: b.SkillsDir, Host: b.Host, CLIVersion: version, CDNBase: cdnBase(), HTTPClient: &http.Client{Timeout: 5 * time.Second}})
		if err != nil {
			return err
		}
	}
	_, err := localHeartbeatSkillsAt(b.SkillsDir, b.Host)
	return err
}

func reportUpdateWatchRecovery(cmd *cobra.Command, cfg *config.Config, manual bool) updateWatchRecovery {
	r := recoverWatchAfterUpdate(cmd, cfg, manual)
	if r.Status == "blocked" || r.Status == "starting" {
		raw, _ := json.Marshal(r)
		fmt.Fprintf(cmd.ErrOrStderr(), "[eigenflux:upgrade-watch] %s\n", raw)
	}
	return r
}

func init() {
	watchCmd.AddCommand(&cobra.Command{Use: "recover", Short: "Restore this account's watch after a CLI upgrade, preserving its binding and disabled tasks", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		r := recoverWatchAfterUpdate(cmd, cfg, true)
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(r); err != nil {
			return err
		}
		if r.Status == "blocked" || r.Status == "starting" {
			return fmt.Errorf("watch recovery incomplete: %s", r.Reason)
		}
		return nil
	}})
}
