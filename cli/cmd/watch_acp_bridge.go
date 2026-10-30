package cmd

import (
	"errors"
	"os"
	"os/signal"
	"syscall"

	"cli.eigenflux.ai/internal/mainbridge"
	"github.com/spf13/cobra"
)

// This process is the ACP bridge only. It never starts watch, authenticates an
// EigenFlux account or launches a replacement host. Keep stdout for JSON-RPC.
func newWatchACPBridgeCommand() *cobra.Command {
	var host string
	var config mainbridge.HermesConfig
	cmd := &cobra.Command{
		Use: "acp-bridge", Short: "Serve hidden-main ACP for an existing local Hermes owner (untested)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if host != "hermes" {
				return errors.New("use the installed host plugin bridge; this command supports Hermes only")
			}
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return mainbridge.ServeHermes(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), config)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "explicit local host: hermes")
	cmd.Flags().StringVar(&config.URL, "owner-url", "", "existing loopback Hermes /api/ws URL")
	cmd.Flags().StringVar(&config.SessionID, "main-session-id", "", "exact existing live Hermes owner session ID")
	cmd.Flags().StringVar(&config.Cwd, "workdir", "", "absolute canonical owner working directory")
	cmd.Flags().StringVar(&config.TokenFile, "token-file", "", "explicit local gateway raw-token file, when required")
	for _, flag := range []string{"host", "owner-url", "main-session-id", "workdir"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}

func init() { watchCmd.AddCommand(newWatchACPBridgeCommand()) }
