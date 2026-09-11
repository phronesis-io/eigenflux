package cmd

import (
	"errors"
	"fmt"
	"os"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var (
	version     string
	commit      string
	serverFlag  string
	formatFlag  string
	homeDirFlag string
	noInteract  bool
	verboseFlag bool
	clientMeta  client.Meta
)

func SetVersion(v string) {
	version = v
}

func SetCommit(c string) {
	commit = c
}

var rootCmd = &cobra.Command{
	Use:   "eigenflux",
	Short: "EigenFlux CLI — agent-oriented information distribution",
	Long: `Command-line interface for the EigenFlux network.
Manage feeds, publish content, send messages, and more.

Usage:
  eigenflux [command]

Examples:
  eigenflux agent provision --draft-file -
  eigenflux feed poll --limit 20
  eigenflux publish --content "New discovery..." --accept-reply
  eigenflux msg send --content "Hello" --item-id 123
  eigenflux server list`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if homeDirFlag != "" {
			config.SetHomeDir(homeDirFlag)
		}
		clientMeta = client.ResolveMeta()
		clientMeta.CLIVersion = version
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&homeDirFlag, "homedir", "", "data directory (default: $EIGENFLUX_HOME or ~/.eigenflux)")
	rootCmd.PersistentFlags().StringVarP(&serverFlag, "server", "s", "", "target server name (default: current server)")
	rootCmd.PersistentFlags().StringVarP(&formatFlag, "format", "f", "", "output format: json, table, or agent (feed poll only: contract preamble + payload). Default: json in non-TTY, table in TTY")
	rootCmd.PersistentFlags().BoolVar(&noInteract, "no-interactive", false, "skip all interactive prompts")
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "verbose stderr logging")

	defaultHelp := rootCmd.HelpFunc()
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		// Cobra prints a group's help whenever the group is not runnable, including when it
		// was reached through an argument it could not resolve. The root command does not
		// behave that way: an unknown command there prints the error by itself. Keep the two
		// consistent, and let Execute report the error.
		if unknownSubcommand(cmd) != nil {
			return
		}
		// Apply --homedir before resolving, since help runs before PersistentPreRun.
		if homeDirFlag != "" {
			config.SetHomeDir(homeDirFlag)
		}
		defaultHelp(cmd, args)
		homeDir, source := config.HomeDirInfo()
		fmt.Fprintf(cmd.OutOrStdout(), "\nHome: %s (%s)\n", homeDir, source)
	})
}

// unknownSubcommand reports an argument that a command group could not resolve.
//
// A group holds subcommands and runs nothing itself. Cobra checks Runnable() before it
// validates arguments, so a group's Args validator is never consulted: `eigenflux profile
// card shwo` printed the group's help and exited 0, which makes a mistyped subcommand in a
// script or a skill indistinguishable from one that did its job. Cobra applies this check
// to the root command only, through legacyArgs. ExecuteC hands back the command it
// resolved, so the leftover argument can be reported here, in the same words and with the
// same suggestions the root command already uses.
func unknownSubcommand(cmd *cobra.Command) error {
	if cmd == nil || cmd.Runnable() || !cmd.HasSubCommands() {
		return nil
	}
	// `--help` on a group is a request for that help, whatever else is on the line.
	if help, err := cmd.Flags().GetBool("help"); err == nil && help {
		return nil
	}
	args := cmd.Flags().Args()
	if len(args) == 0 {
		return nil
	}
	suggestions := ""
	if !cmd.DisableSuggestions {
		// SuggestionsFor compares against this threshold directly; Cobra only defaults it
		// inside its own unexported helper, so an unset group would suggest nothing.
		if cmd.SuggestionsMinimumDistance <= 0 {
			cmd.SuggestionsMinimumDistance = 2
		}
		if candidates := cmd.SuggestionsFor(args[0]); len(candidates) > 0 {
			suggestions = "\n\nDid you mean this?\n"
			for _, candidate := range candidates {
				suggestions += fmt.Sprintf("\t%v\n", candidate)
			}
		}
	}
	return fmt.Errorf("unknown command %q for %q%s", args[0], cmd.CommandPath(), suggestions)
}

// run resolves and runs the command line, returning the error Execute turns into an exit
// code. Kept separate from Execute so tests can exercise the resolution without exiting.
func run() error {
	cmd, err := rootCmd.ExecuteC()
	if err != nil {
		return err
	}
	return unknownSubcommand(cmd)
}

func Execute() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		// Map a server-side 401 to the auth-required exit code so adapters
		// (which key off exit 4) prompt re-login even when the local token
		// looked valid but the server rejected it (revoked / clock skew /
		// server-side expiry). Exit 4 is the single source of truth for "auth".
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 401 {
			os.Exit(output.ExitAuthRequired)
		}
		os.Exit(2)
	}
}
