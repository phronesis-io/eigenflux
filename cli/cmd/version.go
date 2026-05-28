package cmd

import (
	"fmt"
	"runtime"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show CLI version",
	Long: `Display the CLI version and build information.

Examples:
  eigenflux version
  eigenflux version --short`,
	RunE: func(cmd *cobra.Command, args []string) error {
		short, _ := cmd.Flags().GetBool("short")
		if short {
			fmt.Println(version)
			return nil
		}
		homeDir, source := config.HomeDirInfo()
		meta := clientMeta
		info := map[string]string{
			"cli_version": version,
			"commit":      commit,
			"go_version":  runtime.Version(),
			"os":          meta.OS,
			"timezone":    meta.TZ,
			"language":    meta.Lang,
			"host":        meta.Host,
			"channel":     meta.Channel,
			"client_id":   meta.ClientID,
			"home":        homeDir,
			"home_source": string(source),
		}
		format := resolveFormat()
		if format == "table" {
			fmt.Printf("eigenflux CLI %s\n", version)
			fmt.Printf("  Commit:   %s\n", commit)
			fmt.Printf("  Go:       %s\n", runtime.Version())
			fmt.Printf("  OS:       %s\n", meta.OS)
			fmt.Printf("  TZ:       %s\n", meta.TZ)
			fmt.Printf("  Lang:     %s\n", meta.Lang)
			fmt.Printf("  Host:     %s\n", meta.Host)
			fmt.Printf("  Channel:  %s\n", meta.Channel)
			fmt.Printf("  ClientID: %s\n", meta.ClientID)
			fmt.Printf("  Home:     %s (%s)\n", homeDir, source)
			return nil
		}
		output.PrintData(info, format)
		return nil
	},
}

func init() {
	versionCmd.Flags().Bool("short", false, "print only the version number")
	rootCmd.AddCommand(versionCmd)
}
