package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
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
	TargetCommit      string                     `json:"target_commit,omitempty"`
	Status            string                     `json:"status"`
	Mode              string                     `json:"mode,omitempty"`
	Reason            string                     `json:"reason,omitempty"`
	RuntimeReady      bool                       `json:"runtime_ready"`
	AgentCallVerified bool                       `json:"agent_call_verified"`
	DesktopStatus     string                     `json:"desktop_status,omitempty"`
	Notifications     *upgradeNotificationStatus `json:"notifications,omitempty"`
	RunningCommit     string                     `json:"running_commit,omitempty"`
	VersionVerified   bool                       `json:"version_verified"`
	SkillsVerified    bool                       `json:"skills_verified"`
	MainSession       *dispatch.MainNoticeProbe  `json:"main_session,omitempty"`
}

type updateRecoveryDeps struct {
	idle   func() error
	busy   func(dispatch.Binding) (bool, error)
	probe  func(context.Context, dispatch.Binding) dispatch.MainNoticeProbe
	verify func() error
	sync   func(dispatch.Binding) error
	start  func(context.Context, watchservice.NotificationTask) (watchservice.Result, error)
	live   func(dispatch.Binding, bool) watchLiveStatus
}

func recoverUpdatedWatch(ctx context.Context, expected dispatch.Binding, binding *dispatch.Binding, cli string, deps updateRecoveryDeps) updateWatchRecovery {
	r := updateWatchRecovery{Status: "blocked", Mode: "notifications_only", TargetCommit: commit}
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

	running := deps.live(expected, binding == nil).Running
	// Wait for admitted work without killing the owner or replacing its rules.
	if running && deps.busy != nil {
		for {
			busy, err := deps.busy(expected)
			if err != nil {
				r.Reason = "work_state_unavailable"
				return r
			}
			if !busy {
				break
			}
			select {
			case <-ctx.Done():
				r.Status = "pending"
				r.Reason = "active_work_draining"
				return r
			case <-time.After(250 * time.Millisecond):
			}
			if err := deps.verify(); err != nil {
				r.Reason = "identity_changed"
				return r
			}
		}
	}
	if !running && deps.idle != nil {
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
	r.SkillsVerified = true
	if !running {
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

		r.RuntimeReady = live.Running && live.Ready
		r.RunningCommit = live.Commit
		r.VersionVerified = live.Running && live.Commit == commit
		r.AgentCallVerified = live.AgentCallVerified
		r.DesktopStatus = live.DesktopStatus
		if r.RuntimeReady && r.VersionVerified {
			r.Status = "ready"
			if expected.Host == "codex" {
				if deps.probe == nil {
					r.Status = "blocked"
					r.Reason = "codex_main_probe_unavailable"
					return r
				}
				probe := deps.probe(ctx, expected)
				r.MainSession = &probe
				if err := deps.verify(); err != nil {
					r.Status = "blocked"
					r.Reason = "identity_changed"
					return r
				}
				if !probe.TransportAvailable || !probe.TargetMatched || !probe.Enabled {
					r.Status = "blocked"
					r.Reason = probe.Code
				}
			}
			return r
		}
		select {
		case <-ctx.Done():
			r.Reason = "runtime_not_ready"
			if running && !r.VersionVerified {
				r.Status = "pending"
				r.Reason = "version_adoption_pending"
			}
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
		if markWatchRecoveryPending(cfg) != nil {
			r.Reason = "pending_auth_receipt_failed"
		}
		return r
	}
	expected, err := watchBindingIdentity()
	if err != nil {
		r.Status = "blocked"
		if errors.Is(err, os.ErrNotExist) {
			r.Status = "skipped"
			r.Reason = "pending_auth"
			if markWatchRecoveryPending(cfg) != nil {
				r.Reason = "pending_auth_receipt_failed"
			}
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
			if bindingErr != nil || !sameBindingIdentity(currentBinding, *binding) || currentBinding.Revision != binding.Revision || currentBinding.MainSessionID != binding.MainSessionID || !reflect.DeepEqual(currentBinding.Env, binding.Env) {
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
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	r = recoverUpdatedWatch(ctx, expected, binding, cli, updateRecoveryDeps{idle: func() error {
		release, err := watchstate.Acquire(expected.Home, expected.Server)
		if err == nil {
			release()
		}
		return err
	}, busy: func(b dispatch.Binding) (bool, error) {
		if binding == nil {
			return false, nil
		}
		jobs, err := dispatch.ReadJournalStatus(b)
		if err != nil {
			return false, err
		}
		for _, job := range jobs {
			if job.Status == "running" || job.Status == "sending" {
				return true, nil
			}
		}
		return false, nil
	}, probe: func(ctx context.Context, b dispatch.Binding) dispatch.MainNoticeProbe {
		return dispatch.ProbeMainNotice(ctx, dispatch.MainNoticeTarget{Host: b.Host, SessionID: b.MainSessionID, Binding: b})
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
		notifications := upgradeNotifications(cmd, true)
		cfg, configErr := config.Load()
		r := recoverWatchAfterUpdate(cmd, cfg, true)
		if configErr != nil && !errors.Is(configErr, os.ErrNotExist) {
			r.Status = "blocked"
			r.Reason = "configuration_unreadable"
		}
		r.Notifications = &notifications
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(r); err != nil {
			return err
		}
		if r.Status == "blocked" || r.Status == "starting" || r.Status == "pending" {
			return fmt.Errorf("watch recovery incomplete: %s", r.Reason)
		}
		return nil
	}})
}
