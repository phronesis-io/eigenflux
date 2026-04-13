package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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

		// Skip if already migrated
		if _, err := os.Stat(filepath.Join(efHome, "config.json")); err == nil {
			output.PrintMessage("Already migrated — %s/config.json exists", efHome)
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

			// Migrate user settings
			ocSettingsPath := filepath.Join(ocHome, srv.Name, "user_settings.json")
			settingsData, err := os.ReadFile(ocSettingsPath)
			if err == nil {
				var us config.UserSettings
				if json.Unmarshal(settingsData, &us) == nil {
					config.SaveUserSettings(srv.Name, &us)
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
		return nil
	},
}

func init() {
	rootCmd.AddCommand(migrateCmd)
}
