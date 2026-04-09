package cmd

import (
	"fmt"
	"runtime"

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
		info := map[string]string{
			"cli_version": version,
			"go_version":  runtime.Version(),
			"os":          runtime.GOOS,
			"arch":        runtime.GOARCH,
		}
		format := resolveFormat()
		if format == "table" {
			fmt.Printf("eigenflux CLI %s\n", version)
			fmt.Printf("  Go:      %s\n", runtime.Version())
			fmt.Printf("  OS/Arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
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
