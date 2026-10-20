package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/spf13/cobra"
)

func watchBindingIdentity() (dispatch.Binding, error) {
	cfg, err := config.Load()
	if err != nil {
		return dispatch.Binding{}, err
	}
	srv, err := cfg.GetActive(serverFlag)
	if err != nil {
		return dispatch.Binding{}, err
	}
	home, err := watchstate.CanonicalHome(config.HomeDir())
	if err != nil {
		return dispatch.Binding{}, err
	}
	cred, err := auth.LoadV2Credentials(srv.Name)
	if err != nil {
		return dispatch.Binding{}, err
	}
	return dispatch.Binding{Version: 1, Home: home, Server: srv.Name, Endpoint: srv.Endpoint, AgentID: cred.AgentID, PrincipalID: cred.PrincipalID, Scope: watchstate.Scope(home, srv.Name, cred.AgentID, cred.PrincipalID)}, nil
}

func sameBindingIdentity(a, b dispatch.Binding) bool {
	return a.Home == b.Home && a.Server == b.Server && a.Endpoint == b.Endpoint && a.AgentID == b.AgentID && a.PrincipalID == b.PrincipalID && a.Scope == b.Scope
}

// This is opt-in through --watch-managed. A persisted binding remains the owner
// while its foreground process is down, preventing a competing silent takeover.
func applyDispatchOwnership(plan *heartbeatPlan) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	srv, err := cfg.GetActive(serverFlag)
	if err != nil {
		return err
	}
	home, err := watchstate.CanonicalHome(config.HomeDir())
	if err != nil {
		return err
	}
	if _, err := os.Stat(dispatch.BindingPath(home, srv.Name)); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	expected, err := watchBindingIdentity()
	if err != nil {
		return err
	}
	b, err := dispatch.ReadBinding(expected.Home, expected.Server)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !sameBindingIdentity(b, expected) {
		return errWatchIdentity
	}
	plan.DispatchOwned = append([]string{}, b.Events...)
	stages := make([]string, 0, len(plan.ExecutionOrder))
	for _, stage := range plan.ExecutionOrder {
		if (stage == "communication" && b.Handles("pm_push")) || (stage == "commands" && b.Handles("control_pending")) {
			continue
		}
		stages = append(stages, stage)
	}
	plan.ExecutionOrder = stages
	return nil
}

var watchBindCmd = &cobra.Command{
	Use: "bind", Short: "Bind this account to an explicitly configured local Agent", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		file, _ := cmd.Flags().GetString("config")
		if file == "" {
			return updateWatchMainSession(cmd)
		}
		if cmd.Flags().Changed("main-session-id") || cmd.Flags().Changed("current-session") {
			return errors.New("explicit session update and --config are mutually exclusive; put the explicit ID in the configuration")
		}
		expected, err := watchBindingIdentity()
		if err != nil {
			return err
		}
		release, err := lockMainSessionWrite(cmd, expected)
		if err != nil {
			return err
		}
		defer release()
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := dispatch.DecodeBinding(f)
		if err != nil {
			return err
		}
		b.Version, b.Home, b.Server, b.Endpoint, b.AgentID, b.PrincipalID, b.Scope = 1, expected.Home, expected.Server, expected.Endpoint, expected.AgentID, expected.PrincipalID, expected.Scope
		b.Revision, err = dispatch.NewRevision()
		if err != nil {
			return err
		}
		target, err := prepareInitialMainSession(cmd, &b)
		if err != nil {
			return err
		}
		if err = b.Validate(); err != nil {
			return err
		}
		rules, err := localHeartbeatSkillsAt(b.SkillsDir, b.Host)
		if err != nil {
			return fmt.Errorf("bind requires compatible signed Skills: %w", err)
		}
		b.SkillsDir = rules.SkillsDir
		if b.Handles("pm_push") && !fileExistsCLI(filepath.Join(b.SkillsDir, "ef-communication", "references", "dispatch.md")) {
			return errors.New("sync Skills containing the watch dispatch contract before binding")
		}
		if old, readErr := dispatch.ReadBinding(b.Home, b.Server); readErr == nil {
			jobs, err := dispatch.ReadJournalStatus(old)
			if err != nil {
				return err
			}
			for _, j := range jobs {
				switch j.Status {
				case "replied", "no_reply", "completed", "accepted":
				default:
					return errors.New("old binding has unresolved jobs; inspect watch status before replacing it")
				}
			}
		} else if !os.IsNotExist(readErr) {
			return readErr
		}
		preflight, _ := cmd.Flags().GetBool("preflight")
		if preflight {
			return mainSessionPreflight(cmd, target, "", "")
		}
		if target.TaskID != "" {
			previous, _ := cmd.Flags().GetString("expected-id")
			revision, _ := cmd.Flags().GetString("expected-revision")
			if !cmd.Flags().Changed("expected-id") || !cmd.Flags().Changed("expected-revision") || previous != "" || revision != "" {
				return errors.New("new scheduler binding requires empty --expected-id and --expected-revision from preflight")
			}
		}
		// Serialize ownership handoff with legacy notification render/ACK and
		// account switching; no old consumer may ACK after this binding wins.
		if err = auth.WithV2CredentialsLockContext(cmd.Context(), b.Server, 35*time.Second, func() error {
			current, err := watchBindingIdentity()
			if err != nil {
				return err
			}
			if !sameBindingIdentity(expected, current) {
				return errWatchIdentity
			}
			return dispatch.WriteJSON(dispatch.BindingPath(b.Home, b.Server), b)
		}); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"status": "bound", "mode": b.Mode, "host": b.Host, "agent_id": b.AgentID, "binding_revision": b.Revision, "events": b.Events, "execution": "run watch --dispatch after stopping the old consumer"})
	},
}

var watchStatusCmd = &cobra.Command{
	Use: "status", Short: "Read the account binding and redacted execution journal", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		expected, err := watchBindingIdentity()
		if err != nil {
			return err
		}
		b, err := dispatch.ReadBinding(expected.Home, expected.Server)
		if os.IsNotExist(err) {
			owner, ownerErr := readNotificationWatch(expected.Home, expected.Server)
			if ownerErr == nil {
				state := "notifications_configured"
				if !sameBindingIdentity(owner, expected) {
					state = "identity_changed"
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"status": state, "mode": "notifications_only", "agent_id": owner.AgentID, "events": []string{"commission_order"}, "execution_enabled": false, "runtime_verified": false})
			}
			if !os.IsNotExist(ownerErr) {
				return ownerErr
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"status": "unbound"})
		}
		if err != nil {
			return err
		}
		jobs, err := dispatch.ReadJournalStatus(b)
		if err != nil {
			return err
		}
		state := "bound"
		if !sameBindingIdentity(expected, b) {
			state = "identity_changed"
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"status": state, "agent_id": b.AgentID, "host": b.Host, "mode": b.Mode, "events": b.Events, "binding_revision": b.Revision, "main_session_id": b.MainSessionID, "main_notice": dispatch.MainNoticeCapability(b.Host), "main_notice_enabled": false, "workdir": b.WorkDir, "jobs": jobs, "business_verified": false, "commission_subscription": commissionSubscription(b)})
	},
}

var watchDoctorCmd = &cobra.Command{
	Use: "doctor", Short: "Check binding, executable and signed rules without running a model", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		expected, err := watchBindingIdentity()
		if err != nil {
			return err
		}
		b, err := dispatch.ReadBinding(expected.Home, expected.Server)
		if err != nil {
			return err
		}
		if !sameBindingIdentity(expected, b) {
			return errWatchIdentity
		}
		if err = b.Validate(); err != nil {
			return err
		}
		if _, err = localHeartbeatSkillsAt(b.SkillsDir, b.Host); err != nil {
			return err
		}
		result := map[string]any{"status": "configuration_ready", "host": b.Host, "mode": b.Mode, "protocol_checked": false, "main_notice": dispatch.MainNoticeCapability(b.Host), "main_notice_enabled": false, "business_verified": false, "commission_subscription": commissionSubscription(b), "events": b.Events, "workbuddy_local_identity": "unverified"}
		probeMain, _ := cmd.Flags().GetBool("probe-main")
		if probeMain {
			probe := dispatch.ProbeMainNotice(cmd.Context(), dispatch.MainNoticeTarget{Host: b.Host, SessionID: b.MainSessionID, Binding: b})
			currentIdentity, identityErr := watchBindingIdentity()
			currentBinding, bindingErr := dispatch.ReadBinding(b.Home, b.Server)
			if identityErr != nil || bindingErr != nil || !sameBindingIdentity(currentIdentity, b) || !sameBindingIdentity(currentBinding, b) || currentBinding.Revision != b.Revision || currentBinding.MainSessionID != b.MainSessionID || currentBinding.Env["CODEX_HOME"] != b.Env["CODEX_HOME"] {
				return errWatchIdentity
			}
			result["main_notice_probe"] = probe
			result["protocol_checked"] = probe.TransportAvailable
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	},
}

var watchRetryCmd = &cobra.Command{
	Use: "retry [JOB_ID] [--order-id ORDER_ID]", Short: "Explicitly retry a failed or permission-blocked job; unknown results require review", Args: validateWatchRetryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		expected, err := watchBindingIdentity()
		if err != nil {
			return err
		}
		release, err := watchstate.Acquire(expected.Home, expected.Server)
		if err != nil {
			return err
		}
		defer release()
		b, err := dispatch.ReadBinding(expected.Home, expected.Server)
		if err != nil {
			return err
		}
		if !sameBindingIdentity(expected, b) {
			return errWatchIdentity
		}
		q, err := dispatch.OpenJournal(b)
		if err != nil {
			return err
		}
		if orderID, _ := cmd.Flags().GetString("order-id"); orderID != "" {
			return recoverWatchCommission(cmd, b, q, orderID)
		}
		if err = q.Retry(args[0]); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"status": "pending", "job_id": args[0]})
	},
}

var watchReconcileCmd = &cobra.Command{
	Use: "reconcile JOB_ID", Short: "Record an operator-verified outcome for unknown, failed or needs-user work", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		verified, _ := cmd.Flags().GetBool("verified")
		if !verified {
			return errors.New("inspect the Agent execution and business outcome first, then pass --verified; failed confirms optional work did not complete and may be retried")
		}
		expected, err := watchBindingIdentity()
		if err != nil {
			return err
		}
		release, err := watchstate.Acquire(expected.Home, expected.Server)
		if err != nil {
			return err
		}
		defer release()
		b, err := dispatch.ReadBinding(expected.Home, expected.Server)
		if err != nil {
			return err
		}
		if !sameBindingIdentity(expected, b) {
			return errWatchIdentity
		}
		q, err := dispatch.OpenJournal(b)
		if err != nil {
			return err
		}
		action, _ := cmd.Flags().GetString("outcome")
		reply, _ := cmd.Flags().GetString("reply-id")
		if err = q.Reconcile(args[0], action, reply); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"status": action, "job_id": args[0], "evidence": "operator_verified"})
	},
}

func init() {
	watchCmd.Flags().Bool("dispatch", false, "Execute events using this account's local Agent binding (replaces plugin consumption)")
	watchDoctorCmd.Flags().Bool("probe-main", false, "Read existing Codex host connectivity and exact bound target without starting a turn")
	watchBindCmd.Flags().String("config", "", "local Agent binding JSON file; identity is pinned from this account")
	watchRetryCmd.Flags().String("order-id", "", "Recover one seller order absent from notification intake; stop watch first")
	watchReconcileCmd.Flags().Bool("verified", false, "confirm you inspected Agent execution and the business outcome")
	watchReconcileCmd.Flags().String("outcome", "", "verified outcome: PM replied/no_reply; optional event completed/failed (confirmed incomplete, explicit retry allowed)")
	watchReconcileCmd.Flags().String("reply-id", "", "confirmed server message ID; required for replied")
	watchBindCmd.Flags().String("main-session-id", "", "exact existing Codex thread UUID")
	watchBindCmd.Flags().Bool("current-session", false, "explicitly bind the calling Codex thread from CODEX_THREAD_ID")
	watchBindCmd.Flags().String("codex-home", "", "absolute Codex state directory; pins the host store without copying context")
	watchBindCmd.Flags().String("expected-id", "", "previous main session ID; pass an empty value for initial binding")
	watchBindCmd.Flags().String("expected-revision", "", "binding revision from watch status")
	watchBindCmd.Flags().String("scheduler-json", "", "small scheduler inventory JSON; prefer --scheduler-stdin or --scheduler-file")
	watchBindCmd.Flags().Bool("scheduler-stdin", false, "read complete scheduler inventory from stdin")
	watchBindCmd.Flags().String("scheduler-file", "", "read complete scheduler inventory from a JSON file")
	watchBindCmd.Flags().Bool("preflight", false, "validate routing without stopping watch or changing the binding")
	watchBindCmd.Flags().String("expected-task-id", "", "scheduler task ID from preflight; compare with fresh host readback")
	watchBindCmd.Flags().String("expected-thread-id", "", "scheduler target thread from preflight; compare with fresh host readback")
	watchCmd.AddCommand(watchBindCmd, watchStatusCmd, watchDoctorCmd, watchRetryCmd, watchReconcileCmd)
}

func updateWatchMainSession(cmd *cobra.Command) error {
	for _, flag := range []string{"expected-id", "expected-revision"} {
		if !cmd.Flags().Changed(flag) {
			return fmt.Errorf("--%s is required", flag)
		}
	}
	expected, err := watchBindingIdentity()
	if err != nil {
		return err
	}
	release, err := lockMainSessionWrite(cmd, expected)
	if err != nil {
		return err
	}
	defer release()
	b, err := dispatch.ReadBinding(expected.Home, expected.Server)
	if err != nil {
		return err
	}
	if !sameBindingIdentity(b, expected) {
		return errWatchIdentity
	}
	target, err := selectMainSession(cmd, &b)
	if err != nil {
		return err
	}
	previous, _ := cmd.Flags().GetString("expected-id")
	revision, _ := cmd.Flags().GetString("expected-revision")
	b, err = dispatch.SetMainSession(b, revision, previous, target.SessionID)
	if err != nil {
		return err
	}
	if err = b.Validate(); err != nil {
		return err
	}
	preflight, _ := cmd.Flags().GetBool("preflight")
	if preflight {
		return mainSessionPreflight(cmd, target, previous, revision)
	}
	err = auth.WithV2CredentialsLockContext(cmd.Context(), b.Server, 35*time.Second, func() error {
		current, err := watchBindingIdentity()
		if err != nil {
			return err
		}
		if !sameBindingIdentity(expected, current) {
			return errWatchIdentity
		}
		return dispatch.WriteJSON(dispatch.BindingPath(b.Home, b.Server), b)
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"status": "main_session_bound", "previous_session_id": previous, "main_session_id": target.SessionID, "binding_revision": b.Revision})
}
