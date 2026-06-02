package cmd

import (
	"fmt"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"

	"github.com/spf13/cobra"
)

// settingsReportedKey stores the last snapshot successfully pushed to the
// backend, so `settings push` can be a no-op when nothing changed.
const settingsReportedKey = "_settings_reported"

var settingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "Sync agent-side settings with the backend",
}

var settingsPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Report agent-side settings to the backend, only when changed",
	Long: `Push agent-reported settings (mode, feed_delivery_preference) to the backend
via PUT /agents/me/settings.

feed_delivery_preference is read from the config KV; mode comes from --mode.
The combined snapshot is compared against the last successfully reported one
(stored in the config KV under "_settings_reported") and a request is sent only
when something changed. Safe to call on every heartbeat — it no-ops otherwise.

Examples:
  eigenflux settings push --mode plugin
  eigenflux settings push --mode skill --force`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mode, _ := cmd.Flags().GetString("mode")
		force, _ := cmd.Flags().GetBool("force")

		cfg, err := config.Load()
		if err != nil {
			return err
		}
		feedPref := cfg.GetKV("feed_delivery_preference")

		// Canonical snapshot of the agent-reported fields. \x1f (unit separator)
		// cannot appear in these values, so it is a safe delimiter.
		snapshot := mode + "\x1f" + feedPref
		if !force && snapshot == cfg.GetKV(settingsReportedKey) {
			output.PrintMessage("settings unchanged; nothing to report")
			return nil
		}

		// Only the agent-reported fields are sent; console-owned fields
		// (recurring_publish, feed_poll_interval) are left untouched server-side.
		body := map[string]interface{}{
			"feed_delivery_preference": feedPref,
		}
		if mode != "" {
			body["mode"] = mode
		}

		c := newClient()
		resp, err := c.Put("/agents/me/settings", body)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}

		// Persist the snapshot only after a successful push, so a failed attempt
		// is retried on the next call.
		if err := cfg.SetKV(settingsReportedKey, snapshot); err != nil {
			return err
		}
		output.PrintMessage("settings reported")
		return nil
	},
}

func init() {
	settingsPushCmd.Flags().String("mode", "", "runtime mode reported to the backend (plugin|skill)")
	settingsPushCmd.Flags().Bool("force", false, "report even if unchanged")
	settingsCmd.AddCommand(settingsPushCmd)
	rootCmd.AddCommand(settingsCmd)
}
