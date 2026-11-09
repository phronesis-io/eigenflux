package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	watchstate "cli.eigenflux.ai/internal/watch"
	"cli.eigenflux.ai/internal/watchservice"
	"github.com/spf13/cobra"
)

type updateWatchRecovery struct {
	TargetCommit         string                     `json:"target_commit,omitempty"`
	Status               string                     `json:"status"`
	Mode                 string                     `json:"mode,omitempty"`
	Reason               string                     `json:"reason,omitempty"`
	Action               string                     `json:"action,omitempty"`
	Notification         string                     `json:"notification,omitempty"`
	NotificationID       string                     `json:"notification_id,omitempty"`
	RuntimeReady         bool                       `json:"runtime_ready"`
	AgentCallVerified    bool                       `json:"agent_call_verified"`
	DesktopStatus        string                     `json:"desktop_status,omitempty"`
	Notifications        *upgradeNotificationStatus `json:"notifications,omitempty"`
	RunningCommit        string                     `json:"running_commit,omitempty"`
	VersionVerified      bool                       `json:"version_verified"`
	SkillsLatestVerified bool                       `json:"skills_latest_verified"`
	SkillsStatus         string                     `json:"skills_status,omitempty"`
	SkillsVerified       bool                       `json:"skills_verified"`
	MainSession          *dispatch.MainNoticeProbe  `json:"main_session,omitempty"`
}

type updateRecoveryDeps struct {
	ordinaryWatch bool
	idle          func() error
	busy          func(dispatch.Binding) (bool, error)
	probe         func(context.Context, dispatch.Binding) dispatch.MainNoticeProbe
	verify        func() error
	sync          func(dispatch.Binding) error
	start         func(context.Context, watchservice.NotificationTask) (watchservice.Result, error)
	live          func(dispatch.Binding, bool) watchLiveStatus
}

func recoverUpdatedWatch(ctx context.Context, expected dispatch.Binding, binding *dispatch.Binding, cli string, deps updateRecoveryDeps) updateWatchRecovery {
	observation, _ := ctx.Value(watchSetupObservationKey{}).(*outerLoopRecorder)
	stage := func(phase string) {
		if observation != nil {
			observation.stage(phase)
		}
	}
	stage("identity")
	r := updateWatchRecovery{Status: "blocked", Mode: "notifications_only", TargetCommit: commit}
	if deps.ordinaryWatch {
		r.Mode = "watch"
	}
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

	stage("task_prepare")
	notificationsOnly := binding == nil && !deps.ordinaryWatch
	running := deps.live(expected, notificationsOnly).Running
	if !running && deps.ordinaryWatch {
		r.Status, r.Reason = "pending", "runtime_not_ready"
		return r
	}
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
	stage("skills")
	if err := deps.sync(expected); err != nil {
		r.Reason = "skills_not_ready"
		var skillsErr *upgradeSkillsError
		if errors.As(err, &skillsErr) {
			r.Reason = skillsErr.Reason
			if observation != nil {
				observation.failureReason = skillsErr.Reason
			}
		}
		return r
	}
	stage("identity")
	if err := deps.verify(); err != nil {
		r.Reason = "identity_changed"
		return r
	}
	r.SkillsVerified = true
	stage("task_start")
	if !running {
		result, err := deps.start(ctx, watchservice.NotificationTask{CLI: cli, Home: expected.Home, Server: expected.Server, Scope: expected.Scope, AgentID: expected.AgentID, PrincipalID: expected.PrincipalID, Dispatch: binding != nil})
		if err != nil {
			r.Reason = "watch_start_failed"
			if observation != nil {
				observation.failureReason = watchservice.StartFailureReason(err)
			}
			return r
		}
		if result.Status == "skipped_disabled" {
			if observation != nil {
				observation.result, observation.code, observation.knownOutcome = "skipped", "user_disabled", true
			}
			r.Status = "skipped"
			r.Reason = "owner_disabled"
			return r
		}
	}
	if observation != nil {
		observation.result, observation.knownOutcome = "installed", true
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
		live := deps.live(expected, notificationsOnly)

		r.RuntimeReady = live.Running && live.Ready
		r.RunningCommit = live.Commit
		r.VersionVerified = live.Running && live.Commit == commit
		r.AgentCallVerified = !deps.ordinaryWatch && live.AgentCallVerified
		r.DesktopStatus = live.DesktopStatus
		if r.RuntimeReady && r.VersionVerified {
			r.Status = "ready"
			if expected.Host == "codex" && !deps.ordinaryWatch {
				if binding == nil {
					r.Status = "pending"
					r.Reason = "main_session_bootstrap_required"
					return r
				}
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
	r := recoverWatchAfterUpdateOnPlatform(cmd, cfg, manual, runtime.GOOS)
	annotateWatchRecoveryNotice(cmd, cfg, &r)
	return r
}

func recoverWatchAfterUpdateOnPlatform(cmd *cobra.Command, cfg *config.Config, manual bool, goos string) (r updateWatchRecovery) {
	observation := newOuterLoopRecorderWithTrigger(cmd, "watch", "preflight", outerLoopTrigger(manual))
	previous := cmd.Context()
	parent := previous
	if parent == nil {
		parent = context.Background()
	}
	cmd.SetContext(context.WithValue(parent, watchSetupObservationKey{}, observation))
	defer func() {
		if goos == "linux" && watchRecoveryError(r) != nil && r.Action == "" {
			r.Action = "Resolve the reported recovery reason in the existing host or supervisor for this Home/server, then verify its current runtime. Preserve the existing binding and paused state; do not install a second loop."
		}
		switch r.Reason {
		case "platform_not_supported":
			watchSetupOutcome(cmd, "blocked", "unsupported_platform")
		case "external_loop_unverified":
			watchSetupOutcome(cmd, "blocked", "unsupported_platform")
		case "host_managed_unverified":
			watchSetupOutcome(cmd, "skipped", "host_managed")
		case "owner_setup_disabled", "owner_disabled":
			watchSetupOutcome(cmd, "skipped", "user_disabled")
		case "pending_auth":
			watchSetupOutcome(cmd, "skipped", "pending_auth")
		}
		var err error
		if r.Status != "ready" && !observation.knownOutcome {
			err = errors.New("watch recovery incomplete")
		}
		observation.finish(err)
		cmd.SetContext(previous)
	}()
	r = updateWatchRecovery{Status: "skipped", Reason: "platform_not_supported"}
	if os.Getenv("EIGENFLUX_SKIP_AGENT_SETUP") == "1" {
		r.Reason = "owner_setup_disabled"
		return r
	}
	if goos != "darwin" && goos != "windows" && goos != "linux" {
		r.Status = "blocked"
		r.Action = "Verify an existing host-owned persistent loop; automatic watch lifecycle is unavailable on this platform."
		return r
	}
	watchSetupStage(cmd, "identity")
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
	requireLinuxOwner := false
	ordinaryWatch := false
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
			if ordinaryWatch && ownerErr == nil {
				return errWatchConfiguration
			}
			if ownerErr == nil && !sameBindingIdentity(owner, expected) {
				return errWatchIdentity
			}
			if ownerErr != nil && !errors.Is(ownerErr, os.ErrNotExist) {
				return ownerErr
			}
			if requireLinuxOwner && ownerErr != nil {
				return errWatchConfiguration
			}
		}
		return nil
	}
	watchSetupStage(cmd, "binding")
	b, err := dispatch.ReadBinding(expected.Home, expected.Server)
	if err == nil {
		binding = &b
	} else if !errors.Is(err, os.ErrNotExist) {
		r.Status = "blocked"
		r.Reason = "binding_invalid"
		return r
	}
	if goos == "linux" {
		live := watchLiveStatus{}
		liveBinding := expected
		if binding != nil {
			liveBinding = *binding
			live = readWatchLiveStatus(liveBinding, false, time.Now())
		} else if _, err := readNotificationWatch(expected.Home, expected.Server); err == nil {
			requireLinuxOwner = true
			live = readWatchLiveStatus(liveBinding, true, time.Now())
		} else if errors.Is(err, os.ErrNotExist) {
			ordinaryWatch = true
			live = readWatchLiveStatus(liveBinding, false, time.Now())
		}
		if err := verify(); err != nil {
			r.Status, r.Reason = "blocked", "identity_changed"
			return r
		}
		if result, proceed := linuxWatchRecoveryStatus(clientMetaForServerName(expected.Server).Mode, live); !proceed {
			return result
		}
	}
	// A host owns its existing binding. Do not discover or rebind another installed Agent.
	watchSetupStage(cmd, "task_prepare")
	cli, err := stableInstalledExecutable()
	if err != nil {
		r.Status = "blocked"
		r.Reason = "installation_unavailable"
		return r
	}
	syncRules := func(b dispatch.Binding) error { return syncUpgradeWatchRules(cfg, b, manual) }

	if binding == nil {
		expected.Host = clientMetaForServerName(expected.Server).Host
		if expected.Host == "workbuddy" && goos != "linux" {
			watchSetupStage(cmd, "skills")
			if err := syncRules(expected); err != nil {
				r.Status = "blocked"
				r.Reason = "skills_not_ready"
				var detail *upgradeSkillsError
				if errors.As(err, &detail) {
					r.Reason = detail.Reason
					if observation := watchSetupObservation(cmd); observation != nil {
						observation.failureReason = detail.Reason
					}
				}
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
			watchSetupStage(cmd, "binding")
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
	r = recoverUpdatedWatch(ctx, expected, binding, cli, updateRecoveryDeps{ordinaryWatch: ordinaryWatch, idle: func() error {
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
	}, verify: verify, sync: syncRules, start: func(ctx context.Context, task watchservice.NotificationTask) (watchservice.Result, error) {
		if goos == "linux" {
			return watchservice.Result{}, errors.New("restart the existing Linux watch through its supervisor")
		}
		return watchservice.StartNotifications(ctx, task)
	}, live: func(b dispatch.Binding, n bool) watchLiveStatus { return readWatchLiveStatus(b, n, time.Now()) }})
	if r.SkillsVerified {
		r.SkillsLatestVerified = manual || automaticMaintenanceEnabled(cfg, autoSkillSyncKey)
		r.SkillsStatus = "latest_verified"
		if !r.SkillsLatestVerified {
			r.SkillsStatus = "local_verified_auto_sync_disabled"
		}
	}
	if goos == "linux" && r.Status == "ready" {
		watchSetupOutcome(cmd, "skipped", "host_managed")
	}
	return r
}

// Linux lifecycle belongs to the existing host or supervisor. A fresh watch
// pulse can be verified through the ordinary recovery path; configured mode or
// a cached scheduler receipt cannot establish a running loop.
func linuxWatchRecoveryStatus(mode string, live watchLiveStatus) (updateWatchRecovery, bool) {
	if live.Running {
		return updateWatchRecovery{}, true
	}
	r := updateWatchRecovery{
		Status: "pending", Mode: mode, Reason: "external_loop_unverified",
		Action: "Read back the existing host-native or OS scheduler for this Home/server and verify its first unattended cycle and result destination. Preserve paused tasks and reuse the owned trigger; do not create a duplicate. Linux watch lifecycle is not installed by this command.",
	}
	if mode == "plugin" {
		r.Reason = "host_managed_unverified"
		r.Action = "Verify that the existing host plugin owns a persistent loop for this Home/server and completes an unattended cycle. Plugin mode alone does not prove readiness; do not add a second scheduler."
	}
	return r, false
}

func watchRecoveryError(r updateWatchRecovery) error {
	if r.Status == "blocked" || r.Status == "starting" || r.Status == "pending" {
		if r.Action != "" {
			return fmt.Errorf("watch recovery incomplete: %s; %s", r.Reason, r.Action)
		}
		return fmt.Errorf("watch recovery incomplete: %s", r.Reason)
	}
	return nil
}

func reportUpdateWatchRecovery(cmd *cobra.Command, cfg *config.Config, manual bool) updateWatchRecovery {
	r := recoverWatchAfterUpdate(cmd, cfg, manual)
	if r.Status == "blocked" || r.Status == "starting" || r.Status == "pending" && r.Action != "" {
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
		return watchRecoveryError(r)
	}})
}
