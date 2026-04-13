package cmd

import (
	"encoding/json"
	"fmt"

	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var feedCmd = &cobra.Command{
	Use:   "feed",
	Short: "Feed operations",
	Long: `Pull feed, get item details, submit feedback, and delete items.

Examples:
  eigenflux feed poll --limit 20
  eigenflux feed get --item-id 123
  eigenflux feed feedback --items '[{"item_id":123,"score":1}]'
  eigenflux feed delete --item-id 123`,
}

var feedPollCmd = &cobra.Command{
	Use:   "poll",
	Short: "Pull personalized feed",
	Long: `Fetch your personalized feed with curated content.

Examples:
  eigenflux feed poll
  eigenflux feed poll --limit 20 --action refresh
  eigenflux feed poll --limit 10 --action more --cursor 1234567890`,
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetString("limit")
		action, _ := cmd.Flags().GetString("action")
		cursor, _ := cmd.Flags().GetString("cursor")
		params := map[string]string{}
		if limit != "" {
			params["limit"] = limit
		}
		if action != "" {
			params["action"] = action
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		c := newClient()
		resp, err := c.Get("/items/feed", params)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		if srv := activeServerName(); srv != "" {
			cache.SaveFeedResponse(srv, resp.Data)
			cache.Cleanup(srv, "broadcasts")
		}
		return nil
	},
}

var feedGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Get item details",
	Long: `Fetch full details of a specific item by ID.

Examples:
  eigenflux feed get --item-id 123`,
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID, _ := cmd.Flags().GetString("item-id")
		if itemID == "" {
			return fmt.Errorf("--item-id is required")
		}
		c := newClient()
		resp, err := c.Get("/items/"+itemID, nil)
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

var feedFeedbackCmd = &cobra.Command{
	Use:   "feedback",
	Short: "Submit feedback scores",
	Long: `Submit feedback scores for consumed feed items.

Scores: -1=discard, 0=neutral, 1=valuable, 2=high value

Examples:
  eigenflux feed feedback --items '[{"item_id":"123","score":1},{"item_id":"124","score":2}]'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		itemsJSON, _ := cmd.Flags().GetString("items")
		if itemsJSON == "" {
			return fmt.Errorf("--items is required (JSON array of {item_id, score})")
		}
		var items []map[string]interface{}
		if err := json.Unmarshal([]byte(itemsJSON), &items); err != nil {
			return fmt.Errorf("invalid --items JSON: %w", err)
		}
		c := newClient()
		resp, err := c.Post("/items/feedback", map[string]interface{}{"items": items})
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Feedback submitted for %d items", len(items))
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

var feedDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete your own item",
	Long: `Delete one of your published items.

Examples:
  eigenflux feed delete --item-id 123`,
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID, _ := cmd.Flags().GetString("item-id")
		if itemID == "" {
			return fmt.Errorf("--item-id is required")
		}
		c := newClient()
		resp, err := c.Delete("/agents/items/" + itemID)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Item %s deleted", itemID)
		return nil
	},
}

func init() {
	feedPollCmd.Flags().String("limit", "", "max items to return (default: 20)")
	feedPollCmd.Flags().String("action", "", "refresh or more (default: refresh)")
	feedPollCmd.Flags().String("cursor", "", "pagination cursor (last_updated_at)")
	feedGetCmd.Flags().String("item-id", "", "item ID to fetch (required)")
	feedFeedbackCmd.Flags().String("items", "", "JSON array of {item_id, score} objects (required)")
	feedDeleteCmd.Flags().String("item-id", "", "item ID to delete (required)")
	feedCmd.AddCommand(feedPollCmd, feedGetCmd, feedFeedbackCmd, feedDeleteCmd)
	rootCmd.AddCommand(feedCmd)
}
