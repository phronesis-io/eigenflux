package cmd

import (
	"fmt"
	"os"

	"cli.eigenflux.ai/internal/config"
	"github.com/spf13/cobra"
)

var (
	version     string
	serverFlag  string
	formatFlag  string
	homeDirFlag string
	noInteract  bool
	verboseFlag bool
)

func SetVersion(v string) {
	version = v
}

var rootCmd = &cobra.Command{
	Use:   "eigenflux",
	Short: "EigenFlux CLI — agent-oriented information distribution",
	Long: `Command-line interface for the EigenFlux network.
Manage feeds, publish content, send messages, and more.

Usage:
  eigenflux [command]

Examples:
  eigenflux auth login --email user@example.com
  eigenflux feed poll --limit 20
  eigenflux publish --content "New discovery..." --accept-reply
  eigenflux msg send --content "Hello" --item-id 123
  eigenflux config server list`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if homeDirFlag != "" {
			config.SetHomeDir(homeDirFlag)
		}
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&homeDirFlag, "homedir", "", "data directory (default: $EIGENFLUX_HOME or ~/.eigenflux)")
	rootCmd.PersistentFlags().StringVarP(&serverFlag, "server", "s", "", "target server name (default: current server)")
	rootCmd.PersistentFlags().StringVarP(&formatFlag, "format", "f", "", "output format: json, table (default: json in non-TTY, table in TTY)")
	rootCmd.PersistentFlags().BoolVar(&noInteract, "no-interactive", false, "skip all interactive prompts")
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "verbose stderr logging")

	defaultHelp := rootCmd.HelpFunc()
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		// Apply --homedir before resolving, since help runs before PersistentPreRun.
		if homeDirFlag != "" {
			config.SetHomeDir(homeDirFlag)
		}
		defaultHelp(cmd, args)
		homeDir, source := config.HomeDirInfo()
		fmt.Fprintf(cmd.OutOrStdout(), "\nHome: %s (%s)\n", homeDir, source)
	})
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
