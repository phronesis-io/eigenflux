package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
	watchstate "cli.eigenflux.ai/internal/watch"
	"cli.eigenflux.ai/internal/watchservice"
	"cli.eigenflux.ai/internal/workbuddy"
	"github.com/spf13/cobra"
)

func workBuddyBinding(expected dispatch.Binding, cli, workdir, rules string, cfg workbuddy.Config) (dispatch.Binding, error) {
	b := expected
	b.Host, b.Mode, b.WorkDir, b.SkillsDir = "workbuddy", "acp", workdir, rules
	b.Command = []string{cli, "watch", "workbuddy-acp", "--config-dir", cfg.ConfigDir, "--cli-path", cfg.CLIPath, "--node", cfg.Node}
	// Discover the current Sidecar again on each worker invocation: its pipe UUID
	// changes after a host restart. Never persist a stale control-pipe endpoint.
	for _, tool := range workbuddy.DefaultAllowedTools() {
		b.Command = append(b.Command, "--allow-tool", tool)
	}
	b.TimeoutSeconds = 600
	b.Events = []string{"pm_push", "commission_order", "control_pending", "maintenance_due", "profile_review_due"}
	var err error
	b.Revision, err = dispatch.NewRevision()
	if err != nil {
		return b, err
	}
	return b, b.Validate()
}

func startWorkBuddyWatch(cmd *cobra.Command) error {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return errors.New("automatic WorkBuddy watch setup supports macOS and Windows")
	}
	return setupWorkBuddyWatch(cmd, workBuddySetupDeps{discover: workbuddy.Discover, probe: workbuddy.Probe, start: watchservice.StartNotifications, rules: func() (string, error) {
		res, err := localHeartbeatSkills("workbuddy")
		if err != nil {
			return "", err
		}
		return res.SkillsDir, nil
	}})
}

type workBuddySetupDeps struct {
	rules    func() (string, error)
	discover func(workbuddy.Config) (workbuddy.Config, error)
	probe    func(context.Context, workbuddy.Config, string) error
	start    func(context.Context, watchservice.NotificationTask) (watchservice.Result, error)
}

func setupWorkBuddyWatch(cmd *cobra.Command, deps workBuddySetupDeps) error {
	expected, err := watchBindingIdentity()
	if err != nil {
		return err
	}
	if previous, readErr := readNotificationWatch(expected.Home, expected.Server); readErr == nil {
		if !sameBindingIdentity(previous, expected) {
			return errWatchIdentity
		}
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	old, readErr := dispatch.ReadBinding(expected.Home, expected.Server)
	if readErr == nil {
		if !sameBindingIdentity(old, expected) {
			return errWatchIdentity
		}
		if old.Host != "workbuddy" || old.Mode != "acp" || !old.Handles("commission_order") {
			return errors.New("another execution binding owns this account; automatic setup preserves it")
		}
		jobs, err := dispatch.ReadJournalStatus(old)
		if err != nil {
			return err
		}
		for _, job := range jobs {
			if job.Status == "running" || job.Status == "sending" {
				return errors.New("Agent work is active; retry setup after it finishes")
			}
		}
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	rules, err := deps.rules()
	if err != nil {
		return fmt.Errorf("WorkBuddy setup requires synchronized signed Skills: %w", err)
	}
	cfg, err := deps.discover(workbuddy.Config{})
	if err != nil {
		return err
	}
	cli, err := os.Executable()
	if err != nil {
		return err
	}
	workdir := filepath.Join(expected.Home, "workspaces", "workbuddy")
	if readErr == nil {
		workdir = old.WorkDir
	}
	if err = os.MkdirAll(workdir, 0700); err != nil {
		return err
	}
	cfg.AllowedTools = workbuddy.DefaultAllowedTools()
	probeCtx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	err = deps.probe(probeCtx, cfg, workdir)
	cancel()
	if err != nil {
		return fmt.Errorf("WorkBuddy host probe failed; existing task and binding unchanged: %w", err)
	}
	b, err := workBuddyBinding(expected, cli, workdir, rules, cfg)
	if err != nil {
		return err
	}
	if readErr == nil {
		b.Revision = old.Revision
	}
	var ownerRelease func()
	defer func() {
		if ownerRelease != nil {
			ownerRelease()
		}
	}()
	if readErr == nil {
		release, lockErr := watchstate.Acquire(expected.Home, expected.Server)
		if lockErr != nil {
			expected.Revision = old.Revision
			live := readWatchLiveStatus(expected, false, time.Now())
			if live.Running && slices.Equal(old.Command, b.Command) && slices.Equal(old.Events, b.Events) && old.WorkDir == b.WorkDir && old.SkillsDir == b.SkillsDir && len(old.Args) == 0 && len(old.Env) == 0 {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"status": "already_running", "mode": "dispatch", "host": "workbuddy", "agent_id": expected.AgentID, "host_protocol_verified": true, "runtime": live, "business_verified": false})
			}
			return errors.New("existing WorkBuddy watch is running from another configuration; preserve paid work, stop the owned task at a safe maintenance point, then rerun watch start --host workbuddy (upgrade not applied)")
		}
		ownerRelease = release
	}

	task := watchservice.NotificationTask{CLI: cli, Home: expected.Home, Server: expected.Server,
		Scope: expected.Scope, AgentID: expected.AgentID, PrincipalID: expected.PrincipalID, Dispatch: true}
	task.LaunchAgent, _ = cmd.Flags().GetString("launch-agent")
	// Stop only an enabled task owned by this user after the host probe succeeds.
	prepare := task
	prepare.PrepareOnly = true
	if _, err = deps.start(cmd.Context(), prepare); err != nil {
		return err
	}
	if ownerRelease == nil {
		ownerRelease, err = watchstate.Acquire(expected.Home, expected.Server)
		if err != nil {
			return err
		}
	}
	err = auth.WithV2CredentialsLockContext(cmd.Context(), expected.Server, 35*time.Second, func() error {
		current, err := watchBindingIdentity()
		if err != nil {
			return err
		}
		if !sameBindingIdentity(current, expected) {
			return errWatchIdentity
		}
		currentBinding, currentErr := dispatch.ReadBinding(expected.Home, expected.Server)
		if os.IsNotExist(readErr) {
			if !os.IsNotExist(currentErr) {
				return errors.New("binding changed during setup; retry")
			}
		} else if currentErr != nil || currentBinding.Revision != old.Revision || !sameBindingIdentity(currentBinding, expected) {
			return errors.New("binding changed during setup; retry")
		}
		return dispatch.WriteJSON(dispatch.BindingPath(expected.Home, expected.Server), b)
	})
	ownerRelease()
	ownerRelease = nil
	if err != nil {
		return err
	}
	result, err := deps.start(cmd.Context(), task)
	if err != nil {
		return fmt.Errorf("WorkBuddy binding saved; retry watch start --host workbuddy to start its task: %w", err)
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"status": result.Status, "mode": "dispatch", "host": "workbuddy", "agent_id": expected.AgentID, "target": result.Target, "binding_revision": b.Revision, "host_protocol_verified": true, "agent_call_verified": false, "business_verified": false, "action": "check watch status and fresh runtime_ready; validate a real order separately"})
}
