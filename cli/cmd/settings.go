package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"cli.eigenflux.ai/internal/auth"
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

// feedPollIntentKey records the value the user explicitly set for
// feed_poll_interval via `config set`. Only an intent stored here is pushed up
// as a user override; the value pulled down from the backend onboarding ramp
// also lives in the KV (so pollers can read it) but must never be echoed back
// up, or the ramp would freeze the moment any other synced setting changes.
// Cleared after a successful push.
const feedPollIntentKey = "_feed_poll_interval_intent"

// syncedBoolKeys / syncedIntKeys / syncedStringKeys are the config KV entries
// mirrored to the backend agent_settings row (PUT /agents/me/settings).
var (
	syncedBoolKeys   = []string{"recurring_publish", "auto_reply_pm", "official_pm_optout", "auto_comment"}
	syncedIntKeys    = []string{"feed_poll_interval"}
	syncedStringKeys = []string{"feed_delivery_preference"}
)

// isSyncedSettingsKey reports whether a config KV key is mirrored to the
// backend agent_settings row.
func isSyncedSettingsKey(key string) bool {
	for _, k := range append(append(append([]string{}, syncedBoolKeys...), syncedIntKeys...), syncedStringKeys...) {
		if key == k {
			return true
		}
	}
	return false
}

// syncedSettingsBody builds the PUT body from the local config KV. Only keys
// with a usable value are included, so absent keys never clobber the backend.
func syncedSettingsBody(cfg *config.Config) map[string]interface{} {
	body := map[string]interface{}{}
	for _, k := range syncedBoolKeys {
		if v := cfg.GetKV(k); v == "true" || v == "false" {
			body[k] = v == "true"
		}
	}
	for _, k := range syncedIntKeys {
		v := cfg.GetKV(k)
		if k == "feed_poll_interval" {
			// Push only an explicit user intent, never a ramp value pulled down
			// from the backend (which sits in the KV under the same key).
			v = cfg.GetKV(feedPollIntentKey)
		}
		if v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				body[k] = n
				if k == "feed_poll_interval" {
					// Pair the value with an explicit override claim so the
					// backend pins it; without this flag the server keeps the
					// onboarding ramp in effect.
					body["feed_poll_interval_user_set"] = true
				}
			}
		}
	}
	for _, k := range syncedStringKeys {
		if v := cfg.GetKV(k); v != "" {
			body[k] = v
		}
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
		// The intent has been pushed (or there was none); clear it so a later
		// push triggered by a different setting doesn't resend a stale value.
		if err := cfg.SetKV(feedPollIntentKey, ""); err != nil {
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
	// Generic pull: every scalar setting the backend returns is written to the
	// local config KV, so new backend settings need no CLI changes. mode and
	// updated_at are meta, and empty strings never erase a local value that
	// simply has not been pushed yet (e.g. feed_delivery_preference).
	var remote map[string]interface{}
	if err := json.Unmarshal(resp.Data, &remote); err != nil {
		return err
	}
	// mode/updated_at are meta. auto_skill_sync / skill_sync_interval_hours are
	// client-local safety controls (throttle + kill switch for the background
	// skill refresh) and must NOT be settable by the backend — otherwise a
	// compromised backend could disable updates or overflow the throttle.
	skip := map[string]bool{
		"mode": true, "updated_at": true,
		autoSkillSyncKey: true, skillSyncIntervalKey: true,
		// Profile-refresh bookkeeping is client-local for the same reason: a
		// backend-supplied stamp could silence the prompt forever (future
		// value) or force it every day (zero).
		kvProfileRefreshAt: true, kvProfileRefreshCheckedAt: true, kvProfileRefreshPromptedAt: true,
	}
	for k, v := range remote {
		if skip[k] {
			continue
		}
		switch val := v.(type) {
		case bool:
			if err := cfg.SetKV(k, strconv.FormatBool(val)); err != nil {
				return err
			}
		case float64:
			if err := cfg.SetKV(k, strconv.FormatInt(int64(val), 10)); err != nil {
				return err
			}
		case string:
			if val != "" {
				if err := cfg.SetKV(k, val); err != nil {
					return err
				}
			}
		}
	}
	return cfg.SetKV(settingsSyncedKey, "1")
}

// pushReported sends agent-reported fields to the backend, skipping the
// request when nothing changed since the last successful push. Product
// identity is independent from mode and travels through X-Client-Host, the
// existing server-side runtime identity contract.
func pushReported(cfg *config.Config, mode, model, runtimeName, runtimeVersion string, force bool) error {
	feedPref := cfg.GetKV("feed_delivery_preference")
	serverName := activeServerName()
	if serverName == "" {
		return fmt.Errorf("no active server")
	}
	runtimeHost, err := reportedRuntimeHost(runtimeName, runtimeVersion)
	if err != nil {
		return err
	}
	if runtimeHost == "" {
		runtimeHost = clientMeta.Host
	}

	// Canonical snapshot of the agent-reported fields. \x1f (unit separator)
	// cannot appear in these values, so it is a safe delimiter.
	creds, err := auth.LoadCredentials(serverName)
	if err != nil || creds.AgentID == "" {
		return fmt.Errorf("no authenticated account for server %q", serverName)
	}
	snapshot := reportedSettingsSnapshot(creds.AgentID, mode, feedPref, model, runtimeHost)
	lastSnapshot, _, _ := cfg.GetServerOnlyKV(serverName, settingsReportedKey)
	if !force && snapshot == lastSnapshot {
		output.PrintMessage("settings unchanged; nothing to report")
		return nil
	}

	body := map[string]interface{}{
		"feed_delivery_preference": feedPref,
	}
	if mode != "" {
		body["mode"] = mode
	}

	c := newClientForServer(serverName)
	// model is carried as a header (X-Client-Model) so the server stores it
	// alongside the derived runtime, consistent with X-Client-Host.
	headers := map[string]string{}
	if model != "" {
		headers["X-Client-Model"] = model
	}
	if runtimeName != "" {
		headers["X-Client-Host"] = runtimeHost
	}
	resp, err := c.PutWithHeaders("/agents/me/settings", body, headers)
	if err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("%s", resp.Msg)
	}

	// Persist the snapshot only after a successful push, so a failed attempt
	// is retried on the next call.
	if err := cfg.SetServerKV(serverName, settingsReportedKey, snapshot); err != nil {
		return err
	}
	output.PrintMessage("settings reported")
	return nil
}

func reportedSettingsSnapshot(agentID, mode, feedPreference, model, runtimeHost string) string {
	return strings.Join([]string{agentID, mode, feedPreference, model, runtimeHost}, "\x1f")
}

func reportedRuntimeHost(name, version string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	version = strings.ToLower(strings.TrimSpace(version))
	if name == "" {
		if version != "" {
			return "", fmt.Errorf("--runtime-version requires --runtime-name")
		}
		return "", nil
	}
	if name == "terminal" || !validRuntimeIdentityPart(name) {
		return "", fmt.Errorf("--runtime-name must be 1-64 letters, digits, '.', '-' or '_', and cannot be terminal")
	}
	if version == "" {
		return name, nil
	}
	if !validRuntimeIdentityPart(version) {
		return "", fmt.Errorf("--runtime-version must be 1-64 letters, digits, '.', '-' or '_'")
	}
	return name + "/" + version, nil
}

func validRuntimeIdentityPart(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return !(r == '.' || r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z')
	}) < 0
}

var settingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "Sync agent-side settings with the backend",
}

var settingsPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Report agent-side settings to the backend, only when changed",
	Long: `Push agent-reported settings (mode, runtime product, model, feed_delivery_preference) to the backend
via PUT /agents/me/settings.

feed_delivery_preference is read from the config KV; mode, runtime identity, and model come from flags.
The combined snapshot is compared against the last successfully reported one
(stored in the config KV under "_settings_reported") and a request is sent only
when something changed. Safe to call on every heartbeat — it no-ops otherwise.

Examples:
  eigenflux settings push --mode plugin --model gpt-5.6
  eigenflux settings push --mode skill --runtime-name hermes --runtime-version 0.20.0`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mode, _ := cmd.Flags().GetString("mode")
		if mode != "" && mode != "plugin" && mode != "skill" {
			return fmt.Errorf("--mode must be plugin or skill")
		}
		model, _ := cmd.Flags().GetString("model")
		runtimeName, _ := cmd.Flags().GetString("runtime-name")
		runtimeVersion, _ := cmd.Flags().GetString("runtime-version")
		force, _ := cmd.Flags().GetBool("force")

		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return pushReported(cfg, mode, model, runtimeName, runtimeVersion, force)
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
		mode, _ := cmd.Flags().GetString("mode")
		model, _ := cmd.Flags().GetString("model")
		if mode != "" || model != "" {
			return pushReported(cfg, mode, model, "", "", false)
		}
		return nil
	},
}

func init() {
	settingsPushCmd.Flags().String("mode", "", "runtime mode reported to the backend (plugin|skill)")
	settingsPushCmd.Flags().String("model", "", "runtime model reported to the backend, e.g. \"claude-opus-4-8\"")
	settingsPushCmd.Flags().String("runtime-name", "", "Agent product identity reported to the backend, e.g. hermes or workbuddy")
	settingsPushCmd.Flags().String("runtime-version", "", "Agent product version reported with --runtime-name")
	settingsPushCmd.Flags().Bool("force", false, "report even if unchanged")
	settingsSyncCmd.Flags().String("mode", "", "runtime mode reported to the backend (plugin|skill)")
	settingsSyncCmd.Flags().String("model", "", "runtime model reported to the backend, e.g. \"claude-opus-4-8\"")
	settingsCmd.AddCommand(settingsPushCmd)
	settingsCmd.AddCommand(settingsSyncCmd)
	rootCmd.AddCommand(settingsCmd)
}
