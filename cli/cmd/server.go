package cmd

import (
	"fmt"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Manage servers",
	Long: `Add, remove, list, update, and switch between EigenFlux server configurations.

Servers are stored in config.json. Each server has its own credentials,
profile cache, and (optionally) per-server KV entries set via
'eigenflux config set ... --server <name>'.

Examples:
  eigenflux server list
  eigenflux server add --name eigenflux --endpoint https://www.eigenflux.ai
  eigenflux server use --name eigenflux
  eigenflux server remove --name staging`,
}

var serverAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a new server",
	Long: `Add a new server configuration.

Examples:
  eigenflux server add --name eigenflux --endpoint https://www.eigenflux.ai --stream-endpoint wss://stream.eigenflux.ai
  eigenflux server add --name staging --endpoint https://staging.eigenflux.ai`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		endpoint, _ := cmd.Flags().GetString("endpoint")
		streamEndpoint, _ := cmd.Flags().GetString("stream-endpoint")
		commissionEndpoint, _ := cmd.Flags().GetString("commission-endpoint")
		if name == "" || endpoint == "" {
			return fmt.Errorf("--name and --endpoint are required")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if err := cfg.AddServerWithCommission(name, endpoint, streamEndpoint, commissionEndpoint); err != nil {
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
  eigenflux server remove --name staging`,
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
  eigenflux server list`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		type serverEntry struct {
			Name               string `json:"name"`
			Endpoint           string `json:"endpoint"`
			StreamEndpoint     string `json:"stream_endpoint,omitempty"`
			CommissionEndpoint string `json:"commission_endpoint,omitempty"`
			Current            bool   `json:"current"`
		}
		entries := make([]serverEntry, 0, len(cfg.Servers))
		for _, srv := range cfg.Servers {
			entries = append(entries, serverEntry{
				Name:               srv.Name,
				Endpoint:           srv.Endpoint,
				StreamEndpoint:     srv.StreamEndpoint,
				CommissionEndpoint: srv.CommissionEndpoint,
				Current:            srv.Name == cfg.DefaultServer,
			})
		}
		format := resolveFormat()
		if format == "table" {
			fmt.Printf("  %-15s %-32s %-32s %s\n", "NAME", "ENDPOINT", "COMMISSION", "STREAM")
			for _, e := range entries {
				marker := "  "
				if e.Current {
					marker = "* "
				}
				stream := e.StreamEndpoint
				if stream == "" {
					stream = "-"
				}
				commission := e.CommissionEndpoint
				if commission == "" {
					commission = "-"
				}
				fmt.Printf("%s%-15s %-32s %-32s %s\n", marker, e.Name, e.Endpoint, commission, stream)
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
  eigenflux server use --name eigenflux`,
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
  eigenflux server update --name eigenflux --endpoint https://www.eigenflux.ai
  eigenflux server update --name eigenflux --stream-endpoint wss://stream.eigenflux.ai`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		endpoint, _ := cmd.Flags().GetString("endpoint")
		streamEndpoint, _ := cmd.Flags().GetString("stream-endpoint")
		commissionEndpoint, _ := cmd.Flags().GetString("commission-endpoint")
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if err := cfg.UpdateServerWithCommission(name, endpoint, streamEndpoint, commissionEndpoint); err != nil {
			return err
		}
		output.PrintMessage("Server %q updated", name)
		return nil
	},
}

func init() {
	serverAddCmd.Flags().String("name", "", "server name (required)")
	serverAddCmd.Flags().String("endpoint", "", "server endpoint URL (required)")
	serverAddCmd.Flags().String("stream-endpoint", "", "WebSocket stream endpoint (optional, auto-derived from endpoint)")
	serverAddCmd.Flags().String("commission-endpoint", "", "Commission public API endpoint (required for hosted servers)")
	serverRemoveCmd.Flags().String("name", "", "server name to remove (required)")
	serverUseCmd.Flags().String("name", "", "server name to set as default (required)")
	serverUpdateCmd.Flags().String("name", "", "server name to update (required)")
	serverUpdateCmd.Flags().String("endpoint", "", "new endpoint URL")
	serverUpdateCmd.Flags().String("stream-endpoint", "", "WebSocket stream endpoint")
	serverUpdateCmd.Flags().String("commission-endpoint", "", "Commission public API endpoint")

	serverCmd.AddCommand(serverAddCmd, serverRemoveCmd, serverListCmd, serverUseCmd, serverUpdateCmd)
	rootCmd.AddCommand(serverCmd)
}
