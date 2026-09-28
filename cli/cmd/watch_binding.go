package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
		expected, err := watchBindingIdentity()
		if err != nil {
			return err
		}
		release, err := watchstate.Acquire(expected.Home, expected.Server)
		if err != nil {
			return err
		}
		defer release()
		file, _ := cmd.Flags().GetString("config")
		if file == "" {
			return errors.New("--config is required")
		}
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
		if err = dispatch.WriteJSON(dispatch.BindingPath(b.Home, b.Server), b); err != nil {
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
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"status": state, "agent_id": b.AgentID, "host": b.Host, "mode": b.Mode, "events": b.Events, "binding_revision": b.Revision, "workdir": b.WorkDir, "jobs": jobs, "business_verified": false})
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
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"status": "configuration_ready", "host": b.Host, "mode": b.Mode, "protocol_checked": false, "business_verified": false, "workbuddy_local_identity": "unverified"})
	},
}

var watchRetryCmd = &cobra.Command{
	Use: "retry JOB_ID", Short: "Explicitly retry a failed or permission-blocked job; unknown results require review", Args: cobra.ExactArgs(1),
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
	watchBindCmd.Flags().String("config", "", "local Agent binding JSON file; identity is pinned from this account")
	watchReconcileCmd.Flags().Bool("verified", false, "confirm you inspected Agent execution and the business outcome")
	watchReconcileCmd.Flags().String("outcome", "", "verified outcome: PM replied/no_reply; optional event completed/failed (confirmed incomplete, explicit retry allowed)")
	watchReconcileCmd.Flags().String("reply-id", "", "confirmed server message ID; required for replied")
	watchCmd.AddCommand(watchBindCmd, watchStatusCmd, watchDoctorCmd, watchRetryCmd, watchReconcileCmd)
}
