package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"

	"github.com/spf13/cobra"
)

// settingsReportedKey stores the last snapshot successfully pushed to the
// backend, so `settings push` can be a no-op when nothing changed.
const settingsReportedKey = "_settings_reported"

// settingsSyncedKey marks that at least one reconcile with the backend has
// happened. Before that, existing local values initialize the backend instead
// of being overwritten by its defaults.
const settingsSyncedKey = "_settings_synced"

// settingsDirtyKey marks a local write-through that failed to reach the
// backend (e.g. an offline `config set`); the next sync retries the push.
const settingsDirtyKey = "_settings_dirty"

// isSyncedSettingsKey reports whether a config KV key is mirrored to the
// backend agent_settings row (PUT /agents/me/settings).
func isSyncedSettingsKey(key string) bool {
	return key == "recurring_publish" || key == "feed_poll_interval" || key == "feed_delivery_preference"
}

// syncedSettingsBody builds the PUT body from the local config KV. Only keys
// with a usable value are included, so absent keys never clobber the backend.
func syncedSettingsBody(cfg *config.Config) map[string]interface{} {
	body := map[string]interface{}{}
	if v := cfg.GetKV("recurring_publish"); v == "true" || v == "false" {
		body["recurring_publish"] = v == "true"
	}
	if v := cfg.GetKV("feed_poll_interval"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			body["feed_poll_interval"] = n
		}
	}
	if v := cfg.GetKV("feed_delivery_preference"); v != "" {
		body["feed_delivery_preference"] = v
	}
	return body
}

// SyncSettings reconciles the local config KV with the backend settings row.
// Last writer wins through the backend: a pending local change (dirty marker,
// or local values that predate the first sync) is pushed up; otherwise the
// backend values (e.g. console edits) are pulled down. Runs automatically
// after every `feed poll`, so console edits reach the agent within one poll
// interval.
func SyncSettings(cfg *config.Config) error {
	c := newClient()
	pendingLocal := cfg.GetKV(settingsDirtyKey) != "" ||
		(cfg.GetKV(settingsSyncedKey) == "" && len(syncedSettingsBody(cfg)) > 0)
	if pendingLocal {
		if body := syncedSettingsBody(cfg); len(body) > 0 {
			resp, err := c.Put("/agents/me/settings", body)
			if err != nil {
				return err
			}
			if resp.Code != 0 {
				return fmt.Errorf("%s", resp.Msg)
			}
		}
		if err := cfg.SetKV(settingsDirtyKey, ""); err != nil {
			return err
		}
		return cfg.SetKV(settingsSyncedKey, "1")
	}

	resp, err := c.Get("/agents/me/settings", nil)
	if err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("%s", resp.Msg)
	}
	var remote struct {
		RecurringPublish       bool   `json:"recurring_publish"`
		FeedPollInterval       int    `json:"feed_poll_interval"`
		FeedDeliveryPreference string `json:"feed_delivery_preference"`
	}
	if err := json.Unmarshal(resp.Data, &remote); err != nil {
		return err
	}
	if err := cfg.SetKV("recurring_publish", strconv.FormatBool(remote.RecurringPublish)); err != nil {
		return err
	}
	if err := cfg.SetKV("feed_poll_interval", strconv.Itoa(remote.FeedPollInterval)); err != nil {
		return err
	}
	// feed_delivery_preference is agent-owned; never let an empty backend
	// value erase a local preference that simply has not been pushed yet.
	if remote.FeedDeliveryPreference != "" {
		if err := cfg.SetKV("feed_delivery_preference", remote.FeedDeliveryPreference); err != nil {
			return err
		}
	}
	return cfg.SetKV(settingsSyncedKey, "1")
}

// pushReported sends the agent-reported fields (mode, feed_delivery_preference)
// to the backend, skipping the request when nothing changed since the last
// successful push.
func pushReported(cfg *config.Config, mode string, force bool) error {
	feedPref := cfg.GetKV("feed_delivery_preference")

	// Canonical snapshot of the agent-reported fields. \x1f (unit separator)
	// cannot appear in these values, so it is a safe delimiter.
	snapshot := mode + "\x1f" + feedPref
	if !force && snapshot == cfg.GetKV(settingsReportedKey) {
		output.PrintMessage("settings unchanged; nothing to report")
		return nil
	}

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
}

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
		return pushReported(cfg, mode, force)
	},
}

var settingsSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Reconcile local settings with the backend (last writer wins)",
	Long: `Reconcile the config KV settings (recurring_publish, feed_poll_interval,
feed_delivery_preference) with the backend agent_settings row.

A pending local change is pushed up; otherwise the backend values (e.g.
console edits) are pulled down to the local config. This also runs
automatically after every "feed poll", so calling it by hand is only needed
to apply console edits immediately.

Pass --mode to additionally report the runtime mode (see "settings push").

Examples:
  eigenflux settings sync
  eigenflux settings sync --mode skill`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if err := SyncSettings(cfg); err != nil {
			return err
		}
		output.PrintMessage("settings synced")
		if mode, _ := cmd.Flags().GetString("mode"); mode != "" {
			return pushReported(cfg, mode, false)
		}
		return nil
	},
}

func init() {
	settingsPushCmd.Flags().String("mode", "", "runtime mode reported to the backend (plugin|skill)")
	settingsPushCmd.Flags().Bool("force", false, "report even if unchanged")
	settingsSyncCmd.Flags().String("mode", "", "runtime mode reported to the backend (plugin|skill)")
	settingsCmd.AddCommand(settingsPushCmd)
	settingsCmd.AddCommand(settingsSyncCmd)
	rootCmd.AddCommand(settingsCmd)
}
