package cmd

import (
	"encoding/json"

	"cli.eigenflux.ai/internal/watchservice"
	"github.com/spf13/cobra"
)

func newWatchRestartCommand() *cobra.Command {
	var target watchservice.Target
	command := &cobra.Command{
		Use: "restart", Short: "Restart one explicitly selected existing background watch task",
		Long: "Restart the selected OS task using its existing command, account and environment. The task target, not --homedir or --server, selects which watch to restart. Process startup does not verify network or Agent readiness.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := watchservice.Restart(cmd.Context(), target)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	command.Flags().StringVar(&target.LaunchAgent, "launch-agent", "", "macOS: absolute path to an existing plist in this user's ~/Library/LaunchAgents")
	command.Flags().StringVar(&target.ScheduledTask, "scheduled-task", "", "Windows: exact path of an existing current-user interactive scheduled task")
	return command
}

func init() { watchCmd.AddCommand(newWatchRestartCommand()) }
