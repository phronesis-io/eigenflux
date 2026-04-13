package cmd

import (
	"fmt"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage configuration",
	Long: `Manage server connections and user settings.

Examples:
  eigenflux config server list
  eigenflux config set --key recurring_publish --value true
  eigenflux config show`,
}

// ===== Server subcommands =====

var configServerCmd = &cobra.Command{
	Use:   "server",
	Short: "Manage servers",
	Long: `Add, remove, and switch between EigenFlux server configurations.

Examples:
  eigenflux config server list
  eigenflux config server add --name eigenflux --endpoint https://www.eigenflux.ai
  eigenflux config server use --name eigenflux
  eigenflux config server remove --name staging`,
}

var serverAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a new server",
	Long: `Add a new server configuration.

Examples:
  eigenflux config server add --name eigenflux --endpoint https://www.eigenflux.ai --stream-endpoint wss://stream.eigenflux.ai
  eigenflux config server add --name staging --endpoint https://staging.eigenflux.ai`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		endpoint, _ := cmd.Flags().GetString("endpoint")
		streamEndpoint, _ := cmd.Flags().GetString("stream-endpoint")
		if name == "" || endpoint == "" {
			return fmt.Errorf("--name and --endpoint are required")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if err := cfg.AddServerFull(name, endpoint, streamEndpoint); err != nil {
			return err
		}
		output.PrintMessage("Server %q added (%s)", name, endpoint)
		return nil
	},
}

var serverRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove a server",
	Long: `Remove a server configuration and its credentials.

Examples:
  eigenflux config server remove --name staging`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if err := cfg.RemoveServer(name); err != nil {
			return err
		}
		output.PrintMessage("Server %q removed", name)
		return nil
	},
}

var serverListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all servers",
	Long: `List all configured servers and show which is the default.

Examples:
  eigenflux config server list`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		type serverEntry struct {
			Name           string `json:"name"`
			Endpoint       string `json:"endpoint"`
			StreamEndpoint string `json:"stream_endpoint,omitempty"`
			Current        bool   `json:"current"`
		}
		entries := make([]serverEntry, 0, len(cfg.Servers))
		for _, srv := range cfg.Servers {
			entries = append(entries, serverEntry{
				Name:           srv.Name,
				Endpoint:       srv.Endpoint,
				StreamEndpoint: srv.StreamEndpoint,
				Current:        srv.Name == cfg.DefaultServer,
			})
		}
		format := resolveFormat()
		if format == "table" {
			fmt.Printf("  %-15s %-35s %s\n", "NAME", "ENDPOINT", "STREAM")
			for _, e := range entries {
				marker := "  "
				if e.Current {
					marker = "* "
				}
				stream := e.StreamEndpoint
				if stream == "" {
					stream = "-"
				}
				fmt.Printf("%s%-15s %-35s %s\n", marker, e.Name, e.Endpoint, stream)
			}
			return nil
		}
		output.PrintData(entries, format)
		return nil
	},
}

var serverUseCmd = &cobra.Command{
	Use:   "use",
	Short: "Set default server",
	Long: `Switch the default server used by all commands.

Examples:
  eigenflux config server use --name eigenflux`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if err := cfg.SetCurrent(name); err != nil {
			return err
		}
		output.PrintMessage("Switched to server %q", name)
		return nil
	},
}

var serverUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update server configuration",
	Long: `Update an existing server's endpoint.

Examples:
  eigenflux config server update --name eigenflux --endpoint https://www.eigenflux.ai
  eigenflux config server update --name eigenflux --stream-endpoint wss://stream.eigenflux.ai`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		endpoint, _ := cmd.Flags().GetString("endpoint")
		streamEndpoint, _ := cmd.Flags().GetString("stream-endpoint")
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if err := cfg.UpdateServer(name, endpoint, streamEndpoint); err != nil {
			return err
		}
		output.PrintMessage("Server %q updated", name)
		return nil
	},
}

// ===== Settings subcommands =====

var configSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set a user setting",
	Long: `Set a per-server user setting.

Valid keys: recurring_publish (true/false), feed_delivery_preference (text)

Examples:
  eigenflux config set --key recurring_publish --value true
  eigenflux config set --key feed_delivery_preference --value "Push urgent signals immediately"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		key, _ := cmd.Flags().GetString("key")
		value, _ := cmd.Flags().GetString("value")
		if key == "" {
			return fmt.Errorf("--key is required")
		}
		srv := activeServerName()
		if srv == "" {
			return fmt.Errorf("no active server configured")
		}
		settings, err := config.LoadUserSettings(srv)
		if err != nil {
			return err
		}
		if err := settings.Set(key, value); err != nil {
			return err
		}
		if err := config.SaveUserSettings(srv, settings); err != nil {
			return err
		}
		output.PrintMessage("%s = %s", key, value)
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Get a user setting",
	Long: `Get the current value of a per-server user setting.

Valid keys: recurring_publish, feed_delivery_preference

Examples:
  eigenflux config get --key recurring_publish`,
	RunE: func(cmd *cobra.Command, args []string) error {
		key, _ := cmd.Flags().GetString("key")
		if key == "" {
			return fmt.Errorf("--key is required")
		}
		srv := activeServerName()
		if srv == "" {
			return fmt.Errorf("no active server configured")
		}
		settings, err := config.LoadUserSettings(srv)
		if err != nil {
			return err
		}
		val, err := settings.Get(key)
		if err != nil {
			return err
		}
		fmt.Println(val)
		return nil
	},
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show all user settings",
	Long: `Display all per-server user settings.

Examples:
  eigenflux config show`,
	RunE: func(cmd *cobra.Command, args []string) error {
		srv := activeServerName()
		if srv == "" {
			return fmt.Errorf("no active server configured")
		}
		settings, err := config.LoadUserSettings(srv)
		if err != nil {
			return err
		}
		format := resolveFormat()
		if format == "table" {
			rp, _ := settings.Get("recurring_publish")
			fdp, _ := settings.Get("feed_delivery_preference")
			fmt.Printf("%-30s %s\n", "recurring_publish", rp)
			fmt.Printf("%-30s %s\n", "feed_delivery_preference", fdp)
			return nil
		}
		output.PrintData(settings, format)
		return nil
	},
}

func init() {
	// Server flags
	serverAddCmd.Flags().String("name", "", "server name (required)")
	serverAddCmd.Flags().String("endpoint", "", "server endpoint URL (required)")
	serverAddCmd.Flags().String("stream-endpoint", "", "WebSocket stream endpoint (optional, auto-derived from endpoint)")
	serverRemoveCmd.Flags().String("name", "", "server name to remove (required)")
	serverUseCmd.Flags().String("name", "", "server name to set as default (required)")
	serverUpdateCmd.Flags().String("name", "", "server name to update (required)")
	serverUpdateCmd.Flags().String("endpoint", "", "new endpoint URL")
	serverUpdateCmd.Flags().String("stream-endpoint", "", "WebSocket stream endpoint")
	configServerCmd.AddCommand(serverAddCmd, serverRemoveCmd, serverListCmd, serverUseCmd, serverUpdateCmd)

	// Settings flags
	configSetCmd.Flags().String("key", "", "setting key (required)")
	configSetCmd.Flags().String("value", "", "setting value")
	configGetCmd.Flags().String("key", "", "setting key (required)")

	configCmd.AddCommand(configServerCmd, configSetCmd, configGetCmd, configShowCmd)
	rootCmd.AddCommand(configCmd)
}
