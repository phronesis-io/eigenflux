package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"cli.eigenflux.ai/internal/selfupdate"
	"github.com/spf13/cobra"
)

type cliUpdateOutput struct {
	CLIUpdate     selfupdate.Result            `json:"cli_update"`
	CLIVersion    string                       `json:"cli_version"`
	PID           int                          `json:"pid"`
	Home          string                       `json:"home"`
	Observation   maintenanceObservationStatus `json:"observation"`
	OuterLoop     updateWatchRecovery          `json:"outer_loop"`
	Notifications upgradeNotificationStatus    `json:"notifications"`
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check and install the latest signed CLI now",
	Long:  "Immediately check the signed CLI release, preserving signature, checksum and rollback protection. No Agent login is required. An authenticated Home reports maintenance events; otherwise evidence stays local. Automatic-update settings are unchanged.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		var cfg *config.Config
		var configErr error
		if _, err := os.Stat(filepath.Join(config.HomeDir(), "config.json")); err == nil {
			cfg, configErr = config.Load()
		}
		result, restarted, recorder, err := runObservedCLIUpdate(cmd, cfg, "", true)
		if configErr != nil {
			recorder.failure("configuration_unreadable")
		}
		recorder.Flush()
		if err != nil || restarted {
			return err
		}
		recovery := updateWatchRecovery{Status: "skipped", Reason: "cli_update_incomplete"}
		notifications := upgradeNotificationStatus{Status: "skipped", Code: "cli_update_incomplete", Visibility: "unknown"}
		if result.Status == "current" || result.Status == "restarted" {
			notifications = upgradeNotifications(cmd, true)
			recovery = reportUpdateWatchRecovery(cmd, cfg, true)
		}
		output.PrintDataTo(cmd.OutOrStdout(), cliUpdateOutput{
			CLIUpdate: result, CLIVersion: version, PID: os.Getpid(), Home: config.HomeDir(),
			Observation: recorder.Status(), OuterLoop: recovery, Notifications: notifications,
		}, resolveFormat())
		if result.Status != "current" && result.Status != "restarted" {
			return fmt.Errorf("CLI update %s: %s", result.Status, result.Error)
		}
		return nil
	},
}

func init() { rootCmd.AddCommand(updateCmd) }
