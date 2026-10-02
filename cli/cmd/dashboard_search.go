package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

func newDashboardSearchCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "search <query>", Short: "Search your Dashboard messages, friends, broadcasts, services and orders", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		params, err := dashboardSearchParams(cmd, args[0])
		if err != nil {
			return err
		}
		cli, _, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		resp, err := cli.Get("/dashboard/search", params)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("dashboard search failed: %s", resp.Msg)
		}
		var data struct {
			Query  string `json:"query"`
			Groups []struct {
				Type  string `json:"type"`
				Items []struct {
					ID      string `json:"id"`
					Title   string `json:"title"`
					Preview string `json:"preview"`
					Status  string `json:"status"`
					URL     string `json:"url"`
				} `json:"items"`
				NextCursor string `json:"next_cursor"`
				HasMore    bool   `json:"has_more"`
				Error      string `json:"error"`
			} `json:"groups"`
		}
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			return fmt.Errorf("invalid dashboard search response: %w", err)
		}
		if resolveFormat() == "json" {
			var payload interface{}
			if err := json.Unmarshal(resp.Data, &payload); err != nil {
				return err
			}
			output.PrintData(payload, "json")
		} else {
			for _, group := range data.Groups {
				fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n", group.Type)
				if group.Error != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  Error: %s\n", group.Error)
					continue
				}
				for _, item := range group.Items {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s [%s] %s\n    %s\n    %s\n", item.ID, item.Status, item.Title, item.Preview, item.URL)
				}
				if group.HasMore {
					fmt.Fprintf(cmd.OutOrStdout(), "  Next: --type %s --cursor %s\n", group.Type, group.NextCursor)
				}
			}
		}
		for _, group := range data.Groups {
			if group.Error != "" {
				return fmt.Errorf("dashboard search incomplete: %s: %s", group.Type, group.Error)
			}
		}
		return nil
	}}
	cmd.Flags().String("type", "all", "all, message, friend, broadcast, service, or order")
	cmd.Flags().String("status", "", "filter status within one type")
	cmd.Flags().String("cursor", "", "continue one type using its next_cursor")
	cmd.Flags().Int("limit", 10, "results per type (1–50)")
	return cmd
}
func dashboardSearchParams(cmd *cobra.Command, query string) (map[string]string, error) {
	query = strings.TrimSpace(query)
	kind, _ := cmd.Flags().GetString("type")
	status, _ := cmd.Flags().GetString("status")
	cursor, _ := cmd.Flags().GetString("cursor")
	limit, _ := cmd.Flags().GetInt("limit")
	valid := false
	for _, candidate := range []string{"all", "message", "friend", "broadcast", "service", "order"} {
		valid = valid || kind == candidate
	}
	if !valid || !utf8.ValidString(query) || utf8.RuneCountInString(query) < 1 || utf8.RuneCountInString(query) > 100 || strings.ContainsRune(query, 0) || limit < 1 || limit > 50 {
		return nil, fmt.Errorf("provide 1–100 query characters, a valid --type, and --limit 1–50")
	}
	if kind == "all" && (cursor != "" || status != "") {
		return nil, fmt.Errorf("--cursor and --status require one --type")
	}
	if cursor != "" {
		id, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != cursor {
			return nil, fmt.Errorf("invalid --cursor")
		}
	}
	return map[string]string{"q": query, "type": kind, "status": status, "cursor": cursor, "limit": strconv.Itoa(limit)}, nil
}
func init() { dashboardCmd.AddCommand(newDashboardSearchCommand()) }
