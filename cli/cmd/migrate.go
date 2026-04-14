package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

const (
	defaultEndpoint       = "https://www.eigenflux.ai"
	defaultStreamEndpoint = "wss://stream.eigenflux.ai"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate config from OpenClaw plugin",
	Long: `Migrate EigenFlux server configurations and credentials from the
OpenClaw plugin (~/.openclaw/) to the standalone CLI (~/.eigenflux/).

Only runs if OpenClaw config exists and CLI config does not yet exist.
Safe to run multiple times — skips if already migrated.

Examples:
  eigenflux migrate`,
	RunE: func(cmd *cobra.Command, args []string) error {
		home, _ := os.UserHomeDir()
		ocConfigPath := filepath.Join(home, ".openclaw", "openclaw.json")
		efHome := config.HomeDir()

		// Skip migration if already done, but still clear OpenClaw plugin config
		// so the OpenClaw plugin can load (it rejects non-empty server config).
		if _, err := os.Stat(filepath.Join(efHome, "config.json")); err == nil {
			output.PrintMessage("Already migrated — %s/config.json exists", efHome)
			if err := clearOpenClawPluginConfig(ocConfigPath); err != nil {
				output.PrintMessage("Warning: failed to clear OpenClaw plugin config: %v", err)
			}
			return nil
		}

		// Check OpenClaw config exists
		ocData, err := os.ReadFile(ocConfigPath)
		if err != nil {
			output.PrintMessage("No OpenClaw config found at %s", ocConfigPath)
			return nil
		}

		// Parse OpenClaw config
		var ocConfig struct {
			Plugins struct {
				Entries map[string]struct {
					Config struct {
						Servers []struct {
							Name     string `json:"name"`
							Endpoint string `json:"endpoint,omitempty"`
						} `json:"servers"`
					} `json:"config"`
				} `json:"entries"`
			} `json:"plugins"`
		}
		if err := json.Unmarshal(ocData, &ocConfig); err != nil {
			return fmt.Errorf("parse OpenClaw config: %w", err)
		}

		plugin, ok := ocConfig.Plugins.Entries["openclaw-eigenflux"]
		if !ok || len(plugin.Config.Servers) == 0 {
			output.PrintMessage("No eigenflux plugin config found in OpenClaw")
			return nil
		}

		// Build CLI config
		cfg := &config.Config{
			Servers: make([]config.Server, 0, len(plugin.Config.Servers)),
		}

		ocHome := filepath.Join(home, ".openclaw")
		migrated := 0

		for i, srv := range plugin.Config.Servers {
			if srv.Name == "" {
				continue
			}
			endpoint := srv.Endpoint
			if endpoint == "" {
				endpoint = defaultEndpoint
			}
			s := config.Server{
				Name:     srv.Name,
				Endpoint: endpoint,
			}
			if endpoint == defaultEndpoint {
				s.StreamEndpoint = defaultStreamEndpoint
			}

			// Migrate user settings into the server's KV map.
			ocSettingsPath := filepath.Join(ocHome, srv.Name, "user_settings.json")
			if settingsData, err := os.ReadFile(ocSettingsPath); err == nil {
				var us struct {
					RecurringPublish       *bool   `json:"recurring_publish,omitempty"`
					FeedDeliveryPreference *string `json:"feed_delivery_preference,omitempty"`
				}
				if json.Unmarshal(settingsData, &us) == nil {
					if us.RecurringPublish != nil {
						if s.KV == nil {
							s.KV = map[string]string{}
						}
						s.KV["recurring_publish"] = strconv.FormatBool(*us.RecurringPublish)
					}
					if us.FeedDeliveryPreference != nil && *us.FeedDeliveryPreference != "" {
						if s.KV == nil {
							s.KV = map[string]string{}
						}
						s.KV["feed_delivery_preference"] = *us.FeedDeliveryPreference
					}
				}
			}

			cfg.Servers = append(cfg.Servers, s)
			if i == 0 {
				cfg.DefaultServer = srv.Name
			}

			// Migrate credentials
			ocCredsPath := filepath.Join(ocHome, srv.Name, "credentials.json")
			credsData, err := os.ReadFile(ocCredsPath)
			if err == nil {
				var creds auth.Credentials
				if json.Unmarshal(credsData, &creds) == nil && creds.AccessToken != "" {
					auth.SaveCredentials(srv.Name, &creds)
					migrated++
				}
			}
		}

		if len(cfg.Servers) == 0 {
			output.PrintMessage("No servers to migrate")
			return nil
		}

		if err := cfg.Save(); err != nil {
			return fmt.Errorf("save config: %w", err)
		}

		for _, s := range cfg.Servers {
			marker := "  "
			if s.Name == cfg.DefaultServer {
				marker = "* "
			}
			output.PrintMessage("%s%s (%s)", marker, s.Name, s.Endpoint)
		}
		output.PrintMessage("Migrated %d server(s), %d credential(s)", len(cfg.Servers), migrated)

		if err := clearOpenClawPluginConfig(ocConfigPath); err != nil {
			output.PrintMessage("Warning: failed to clear OpenClaw plugin config: %v", err)
		}
		return nil
	},
}

// clearOpenClawPluginConfig resets plugins.entries["openclaw-eigenflux"].config
// to an empty object in openclaw.json. OpenClaw refuses to load the plugin when
// the eigenflux config still contains server entries, so we wipe it after the
// CLI has migrated the data to ~/.eigenflux/. Other fields are preserved.
func clearOpenClawPluginConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	plugins, _ := raw["plugins"].(map[string]any)
	if plugins == nil {
		return nil
	}
	entries, _ := plugins["entries"].(map[string]any)
	if entries == nil {
		return nil
	}
	entry, ok := entries["openclaw-eigenflux"].(map[string]any)
	if !ok {
		return nil
	}
	if existing, ok := entry["config"].(map[string]any); ok && len(existing) == 0 {
		return nil
	}
	entry["config"] = map[string]any{}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}

func init() {
	rootCmd.AddCommand(migrateCmd)
}
