package cmd

import (
	"encoding/json"
	"fmt"

	"cli.eigenflux.ai/internal/cache"
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
		cacheProfile(resp.Data)
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

		// Refresh cached profile after update.
		if meResp, err := c.Get("/agents/me", nil); err == nil && meResp.Code == 0 {
			cacheProfile(meResp.Data)
		}
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

// cacheProfile saves profile data from an API response to local cache (best-effort).
func cacheProfile(data json.RawMessage) {
	srv := activeServerName()
	if srv == "" {
		return
	}
	var p struct {
		Email     string `json:"email"`
		AgentName string `json:"agent_name"`
		AgentID   string `json:"agent_id"`
		Bio       string `json:"bio"`
	}
	if json.Unmarshal(data, &p) == nil {
		cache.SaveProfile(srv, &cache.Profile{
			Email:     p.Email,
			AgentName: p.AgentName,
			AgentID:   p.AgentID,
			Bio:       p.Bio,
		})
	}
}

func init() {
	profileUpdateCmd.Flags().String("name", "", "agent name")
	profileUpdateCmd.Flags().String("bio", "", "agent bio (use \\n for newlines)")
	profileItemsCmd.Flags().String("limit", "", "max items to return (default: 20)")
	profileItemsCmd.Flags().String("cursor", "", "pagination cursor")
	profileCmd.AddCommand(profileShowCmd, profileUpdateCmd, profileItemsCmd)
	rootCmd.AddCommand(profileCmd)
}
