package cmd

import (
	"os"
	"os/signal"
	"syscall"

	"cli.eigenflux.ai/internal/workbuddy"
	"github.com/spf13/cobra"
)

// This is a background execution bridge. It does not advertise the hidden-main
// extension: native input metadata alone does not establish Desktop UI delivery.
// Keep this transport helper out of interactive capability discovery.
func newWatchWorkBuddyACPCommand() *cobra.Command {
	var config workbuddy.Config
	cmd := &cobra.Command{
		Use: "workbuddy-acp", Hidden: true, Short: "Serve ACP through the local WorkBuddy host (macOS/Windows; Windows acceptance pending)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return workbuddy.Serve(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), config)
		},
	}
	cmd.Flags().StringVar(&config.ConfigDir, "config-dir", "", "absolute WorkBuddy configuration directory (default ~/.workbuddy)")
	cmd.Flags().StringVar(&config.ControlSocket, "control-socket", "", "explicit host Sidecar socket; otherwise discover the unique local socket")
	cmd.Flags().StringVar(&config.CLIPath, "cli-path", "", "absolute official bundled WorkBuddy CLI launcher")
	cmd.Flags().StringVar(&config.Node, "node", "", "absolute Node executable (default PATH)")
	cmd.Flags().StringArrayVar(&config.Tools, "tool", nil, "restrict available built-in tools to these explicit names; does not grant permissions")
	cmd.Flags().StringArrayVar(&config.AllowedTools, "allow-tool", nil, "explicit host tool allow rules; other permission requests remain subject to the ACP caller")
	return cmd
}

func init() { watchCmd.AddCommand(newWatchWorkBuddyACPCommand()) }
