package cmd

import (
	"fmt"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage key-value configuration",
	Long: `Manage free-form key-value configuration stored in config.json.

Keys and values are arbitrary strings. There are two scopes:
  - Global (no --server):     stored under the top-level "kv".
  - Per-server (--server X):  stored under servers[X].kv.

On get, a per-server read (--server) falls back to the global "kv" if
the key is not set on that server. Setting a key to an empty value
deletes it. Use 'eigenflux server ...' to manage server configurations.

Examples:
  eigenflux config set --key recurring_publish --value true
  eigenflux config set --key feed_delivery_preference --value "Push urgent signals immediately"
  eigenflux config set --key plugin_version --value 1.2.0 --server staging
  eigenflux config get --key plugin_version --server staging
  eigenflux config show`,
}

var configSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set a config key",
	Long: `Set a free-form key-value entry in config.json.

  - no --server: stored globally.
  - --server NAME: stored under that server.
An empty value deletes the entry.

Backend-synced settings (feed_delivery_preference, recurring_publish,
auto_reply_pm, feed_poll_interval) are account-level and always stored
globally; --server is ignored for them (and any stray per-server copy is
cleared) so they reliably reach the backend.

Examples:
  eigenflux config set --key recurring_publish --value true
  eigenflux config set --key plugin_version --value 1.2.0
  eigenflux config set --key plugin_version --value 1.3.0 --server staging`,
	RunE: func(cmd *cobra.Command, args []string) error {
		key, _ := cmd.Flags().GetString("key")
		value, _ := cmd.Flags().GetString("value")
		if key == "" {
			return fmt.Errorf("--key is required")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		synced := isSyncedSettingsKey(key)
		// Backend-synced settings are account-level and live only in the
		// global KV: the sync layer (settings push / sync) reads global-only,
		// so a per-server copy would never reach the backend — it would just
		// shadow reads via GetServerKV's fallback and look applied. Force them
		// global regardless of --server, and scrub any stray server-scoped
		// copy so a previously misplaced value can self-heal.
		if serverFlag != "" && !synced {
			if err := cfg.SetServerKV(serverFlag, key, value); err != nil {
				return err
			}
			output.PrintMessage("%s = %s (server %q)", key, value, serverFlag)
			return nil
		}
		if serverFlag != "" && synced {
			output.PrintMessage("note: %q is a backend-synced setting; stored globally (--server %q ignored)", key, serverFlag)
			if err := cfg.ClearServerScopedKV(key); err != nil {
				return err
			}
		}
		if err := cfg.SetKV(key, value); err != nil {
			return err
		}
		output.PrintMessage("%s = %s", key, value)
		// Write-through for backend-mirrored settings so the console and the
		// agent stay in sync (last writer wins). A failed push is retried on
		// the next sync via the dirty marker.
		if synced {
			if err := cfg.SetKV(settingsDirtyKey, "1"); err != nil {
				return err
			}
			if err := SyncSettings(cfg); err != nil {
				output.PrintMessage("(backend sync deferred: %v)", err)
			}
		}
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Get a config value",
	Long: `Read a free-form key-value entry from config.json.

  - no --server: reads the global "kv".
  - --server NAME: reads that server's "kv", falling back to the
    global "kv" if the key is not set on the server.

Examples:
  eigenflux config get --key recurring_publish
  eigenflux config get --key plugin_version --server staging`,
	RunE: func(cmd *cobra.Command, args []string) error {
		key, _ := cmd.Flags().GetString("key")
		if key == "" {
			return fmt.Errorf("--key is required")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if serverFlag != "" {
			val, _, err := cfg.GetServerKV(serverFlag, key)
			if err != nil {
				return err
			}
			fmt.Println(val)
			return nil
		}
		fmt.Println(cfg.GetKV(key))
		return nil
	},
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show all key-value entries",
	Long: `Display both the global "kv" and the active (or --server-selected)
server's "kv" map from config.json.

Examples:
  eigenflux config show
  eigenflux config show --server staging`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		activeSrv, _ := cfg.GetActive(serverFlag)
		var serverName string
		var serverKV map[string]string
		if activeSrv != nil {
			serverName = activeSrv.Name
			serverKV = activeSrv.KV
		}
		format := resolveFormat()
		if format == "table" {
			for k, v := range serverKV {
				fmt.Printf("%-30s %s  (server %q)\n", k, v, serverName)
			}
			for k, v := range cfg.KV {
				fmt.Printf("%-30s %s  (global)\n", k, v)
			}
			return nil
		}
		out := struct {
			Server   string            `json:"server,omitempty"`
			ServerKV map[string]string `json:"server_kv,omitempty"`
			KV       map[string]string `json:"kv,omitempty"`
		}{serverName, serverKV, cfg.KV}
		output.PrintData(out, format)
		return nil
	},
}

func init() {
	configSetCmd.Flags().String("key", "", "config key (required)")
	configSetCmd.Flags().String("value", "", "config value (empty deletes)")
	configGetCmd.Flags().String("key", "", "config key (required)")

	configCmd.AddCommand(configSetCmd, configGetCmd, configShowCmd)
	rootCmd.AddCommand(configCmd)
}
