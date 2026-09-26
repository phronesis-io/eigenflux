package cmd

import (
	"bytes"
	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/output"
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"os"
	"strings"
)

func discoveryCall(c *cobra.Command, method, path string, body any, params map[string]string, save bool) error {
	server := activeServerName()
	client, _, err := newV2ClientForServer(server, true)
	if err != nil {
		return err
	}
	key, _ := c.Flags().GetString("idempotency-key")
	var data json.RawMessage
	switch method {
	case "GET":
		r, e := client.Get(path, params)
		if e != nil {
			return e
		}
		if r.Code != 0 {
			return fmt.Errorf("%s", r.Msg)
		}
		data = r.Data
	default:
		r, e := client.PostWithHeaders(path, body, map[string]string{"Idempotency-Key": key})
		if e != nil {
			return e
		}
		if r.Code != 0 {
			return fmt.Errorf("%s", r.Msg)
		}
		data = r.Data
	}
	if save {
		cache.SaveFeedResponse(server, data)
		cache.Cleanup(server, "broadcasts")
	}
	output.PrintData(data, resolveFormat())
	return nil
}
func discoveryFile(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) > 64<<10 {
		return nil, fmt.Errorf("JSON file exceeds 64 KiB")
	}
	var v map[string]any
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err = decoder.Decode(&v); err != nil {
		return nil, err
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("expected a single JSON object")
	}
	if v == nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	return v, nil
}
func newDiscoveryCommands() []*cobra.Command {
	search := &cobra.Command{Use: "search <query>", Short: "Search broadcasts, services, and public Agents", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		if strings.TrimSpace(args[0]) == "" {
			return fmt.Errorf("query must not be blank")
		}
		body := map[string]any{"query": args[0]}
		types, _ := c.Flags().GetStringSlice("types")
		if len(types) > 0 {
			body["source_kinds"] = types
		}
		limit, _ := c.Flags().GetInt("limit")
		body["limit"] = limit
		cursor, _ := c.Flags().GetString("cursor")
		if cursor != "" {
			body["cursor"] = cursor
		}
		f, _ := c.Flags().GetString("filters")
		if f != "" {
			v, err := discoveryFile(f)
			if err != nil {
				return err
			}
			body["filters"] = v
		}
		return discoveryCall(c, "POST", "/discovery/search", body, nil, true)
	}}
	search.Flags().String("filters", "", "Explicit hard filters JSON file")
	search.Flags().StringSlice("types", nil, "broadcast,commission,agent")
	search.Flags().Int("limit", 20, "Results per page, maximum 50")
	search.Flags().String("cursor", "", "Next-page cursor; keep the query and other options unchanged")
	search.Flags().String("idempotency-key", "", "Retry key")
	recommend := &cobra.Command{Use: "recommend", Short: "Find relevant results based on your current interests", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		types, _ := c.Flags().GetStringSlice("types")
		limit, _ := c.Flags().GetInt("limit")
		v := map[string]any{"limit": limit}
		if len(types) > 0 {
			v["source_kinds"] = types
		}
		return discoveryCall(c, "POST", "/discovery/recommendations", v, nil, true)
	}}
	recommend.Flags().Int("limit", 20, "Maximum results, up to 100; fewer may match")
	recommend.Flags().StringSlice("types", nil, "Source kinds")
	recommend.Flags().String("idempotency-key", "", "Retry key")
	taxonomy := &cobra.Command{Use: "taxonomy", Short: "Look up canonical intents"}
	lookup := &cobra.Command{Use: "search <phrase>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		cat, _ := c.Flags().GetString("category")
		sub, _ := c.Flags().GetString("subtype")
		return discoveryCall(c, "GET", "/taxonomy/search", nil, map[string]string{"query": args[0], "category": cat, "subtype": sub}, false)
	}}
	lookup.Flags().String("category", "", "Canonical category")
	lookup.Flags().String("subtype", "", "Canonical subtype")
	taxonomy.AddCommand(lookup)
	return []*cobra.Command{search, recommend, taxonomy}
}

func init() { rootCmd.AddCommand(newDiscoveryCommands()...) }
