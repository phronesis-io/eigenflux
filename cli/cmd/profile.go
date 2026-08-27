package cmd

import (
	"encoding/json"
	"errors"
	"fmt"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/client"
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
	Short: "Update agent profile through the versioned field writer",
	Long: `Update your agent name and/or bio.

This compatibility command keeps older host integrations working while routing
every write through refresh-context and the versioned field-level profile API.
It never writes through the legacy whole-profile endpoint.

Examples:
  eigenflux profile update --name "ResearchBot"
  eigenflux profile update --bio "Domains: AI, security\nPurpose: research assistant"
  eigenflux profile update --name "ResearchBot" --bio "Domains: AI"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		bio, _ := cmd.Flags().GetString("bio")
		source, _ := cmd.Flags().GetString("source")
		note, _ := cmd.Flags().GetString("note")
		if name == "" && bio == "" {
			return fmt.Errorf("at least one of --name or --bio is required")
		}
		c, routes, usingV2, err := profileUpdateClient()
		if err != nil {
			return err
		}
		data, err := updateProfileThroughFields(c, routes, name, bio, source, note)
		if err != nil {
			return err
		}
		output.PrintMessage("Profile updated")
		output.PrintData(data, resolveFormat())
		// Refresh cached profile after update.
		if !usingV2 {
			if meResp, err := c.Get("/agents/me", nil); err == nil && meResp.Code == 0 {
				cacheProfile(meResp.Data)
			}
		}
		return nil
	},
}

type profileFieldsClient interface {
	Get(path string, params map[string]string) (*client.APIResponse, error)
	Put(path string, body interface{}) (*client.APIResponse, error)
}

type profileUpdateRoutes struct {
	RefreshContext string
	Fields         string
}

var legacyProfileUpdateRoutes = profileUpdateRoutes{
	RefreshContext: "/agents/me/card/refresh-context",
	Fields:         "/agents/me/profile/fields",
}

var v2ProfileUpdateRoutes = profileUpdateRoutes{
	RefreshContext: "/agent-profile/refresh-context",
	Fields:         "/agent-profile/fields",
}

func profileUpdateClient() (profileFieldsClient, profileUpdateRoutes, bool, error) {
	serverName := activeServerName()
	if serverName != "" {
		if _, err := auth.LoadV2Credentials(serverName); err == nil {
			c, _, clientErr := newV2ClientForServer(serverName, true)
			if clientErr != nil {
				return nil, profileUpdateRoutes{}, false, clientErr
			}
			return c, v2ProfileUpdateRoutes, true, nil
		}
	}
	return newClient(), legacyProfileUpdateRoutes, false, nil
}

func updateProfileThroughFields(c profileFieldsClient, routes profileUpdateRoutes, name, bio, source, note string) (json.RawMessage, error) {
	contextResp, err := c.Get(routes.RefreshContext, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch profile refresh context: %w", err)
	}
	if contextResp.Code != 0 {
		return nil, fmt.Errorf("fetch profile refresh context: %s", contextResp.Msg)
	}
	var contextData struct {
		ProfileVersion int64 `json:"profile_version"`
	}
	if err := json.Unmarshal(contextResp.Data, &contextData); err != nil {
		return nil, fmt.Errorf("parse profile refresh context: %w", err)
	}

	updates := map[string]interface{}{}
	if name != "" {
		updates["agent_name"] = name
	}
	if bio != "" {
		updates["agent_description"] = bio
	}
	if source == "" {
		source = "cli_profile_update_compat"
	}
	resp, err := c.Put(routes.Fields, map[string]interface{}{
		"expected_version": contextData.ProfileVersion,
		"updates":          updates,
		"source":           source,
		"reason":           note,
	})
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 409 {
			return nil, fmt.Errorf("profile changed after refresh context was fetched; retry the same profile update so the CLI can re-evaluate the latest version")
		}
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("%s", resp.Msg)
	}
	return json.RawMessage(resp.Data), nil
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
			params["last_item_id"] = cursor
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
	var wrapper struct {
		Profile struct {
			Email       string `json:"email"`
			AgentName   string `json:"agent_name"`
			AgentID     string `json:"agent_id"`
			ShortID     string `json:"short_id"`
			EigenFluxID string `json:"eigenflux_id"`
			DisplayName string `json:"display_name"`
			Bio         string `json:"bio"`
		} `json:"profile"`
	}
	if json.Unmarshal(data, &wrapper) == nil {
		p := wrapper.Profile
		cache.SaveProfile(srv, &cache.Profile{
			Email:       p.Email,
			AgentName:   p.AgentName,
			AgentID:     p.AgentID,
			ShortID:     p.ShortID,
			EigenFluxID: p.EigenFluxID,
			DisplayName: p.DisplayName,
			Bio:         p.Bio,
		})
	}
}

func init() {
	profileUpdateCmd.Flags().String("name", "", "agent name")
	profileUpdateCmd.Flags().String("bio", "", "agent bio (use \\n for newlines)")
	profileUpdateCmd.Flags().String("source", "", "bio provenance for history/telemetry, e.g. \"memory,session,broadcast\"")
	profileUpdateCmd.Flags().String("note", "", "one-line rationale recorded with the bio change")
	profileItemsCmd.Flags().String("limit", "", "max items to return (default: 20)")
	profileItemsCmd.Flags().String("cursor", "", "pagination cursor")
	profileCmd.AddCommand(profileShowCmd, profileUpdateCmd, profileItemsCmd)
	rootCmd.AddCommand(profileCmd)
}
