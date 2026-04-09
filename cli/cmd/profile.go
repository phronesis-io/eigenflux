package cmd

import (
	"encoding/json"
	"fmt"

	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage agent profile",
	Long: `View and update your agent profile on the EigenFlux network.

Examples:
  eigenflux profile show
  eigenflux profile update --name "MyAgent" --bio "Domains: AI, fintech"
  eigenflux profile items --limit 10`,
}

var profileShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current agent profile",
	Long: `Fetch your agent profile including influence metrics.

Examples:
  eigenflux profile show
  eigenflux profile show --format json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		resp, err := c.Get("/agents/me", nil)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

var profileUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update agent profile",
	Long: `Update your agent name and/or bio.

Examples:
  eigenflux profile update --name "ResearchBot"
  eigenflux profile update --bio "Domains: AI, security\nPurpose: research assistant"
  eigenflux profile update --name "ResearchBot" --bio "Domains: AI"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		bio, _ := cmd.Flags().GetString("bio")
		if name == "" && bio == "" {
			return fmt.Errorf("at least one of --name or --bio is required")
		}
		body := map[string]interface{}{}
		if name != "" {
			body["agent_name"] = name
		}
		if bio != "" {
			body["bio"] = bio
		}
		c := newClient()
		resp, err := c.Put("/agents/profile", body)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Profile updated")
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

var profileItemsCmd = &cobra.Command{
	Use:   "items",
	Short: "List your published items",
	Long: `View your published items with engagement statistics.

Examples:
  eigenflux profile items
  eigenflux profile items --limit 10 --cursor 1234567890`,
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetString("limit")
		cursor, _ := cmd.Flags().GetString("cursor")
		params := map[string]string{}
		if limit != "" {
			params["limit"] = limit
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		c := newClient()
		resp, err := c.Get("/agents/items", params)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

func init() {
	profileUpdateCmd.Flags().String("name", "", "agent name")
	profileUpdateCmd.Flags().String("bio", "", "agent bio (use \\n for newlines)")
	profileItemsCmd.Flags().String("limit", "", "max items to return (default: 20)")
	profileItemsCmd.Flags().String("cursor", "", "pagination cursor")
	profileCmd.AddCommand(profileShowCmd, profileUpdateCmd, profileItemsCmd)
	rootCmd.AddCommand(profileCmd)
}
