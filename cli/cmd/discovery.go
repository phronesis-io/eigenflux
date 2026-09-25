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
	"strconv"
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
	case "PUT":
		r, e := client.PutWithHeaders(path, body, map[string]string{"Idempotency-Key": key})
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
func validDiscoveryID(s string) error {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("ID must be a positive signed-64-bit decimal string")
	}
	return nil
}
func newDiscoveryCommands() []*cobra.Command {
	search := &cobra.Command{Use: "search [query]", Short: "Search broadcasts, services, and public Agents", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		body := map[string]any{}
		inputs := 0
		need, _ := c.Flags().GetString("need")
		file, _ := c.Flags().GetString("file")
		if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
			body["query"] = args[0]
			inputs++
		}
		if need != "" {
			if err := validDiscoveryID(need); err != nil {
				return err
			}
			body["need_id"] = need
			inputs++
		}
		if file != "" {
			v, err := discoveryFile(file)
			if err != nil {
				return err
			}
			body["need"] = v
			inputs++
		}
		if inputs != 1 {
			return fmt.Errorf("provide exactly one query, --need, or --file")
		}
		types, _ := c.Flags().GetStringSlice("types")
		if len(types) > 0 {
			body["source_kinds"] = types
		}
		limit, _ := c.Flags().GetInt("limit")
		body["limit"] = limit
		f, _ := c.Flags().GetString("filters")
		if f != "" {
			v, err := discoveryFile(f)
			if err != nil {
				return err
			}
			body["filters"] = v
		}
		return discoveryCall(c, "POST", "/api/v2/discovery/search", body, nil, true)
	}}
	search.Flags().String("need", "", "Saved Need ID")
	search.Flags().String("file", "", "Inline Need JSON file")
	search.Flags().String("filters", "", "Explicit hard filters JSON file")
	search.Flags().StringSlice("types", nil, "broadcast,commission,agent")
	search.Flags().Int("limit", 20, "Total results, maximum 50")
	search.Flags().String("idempotency-key", "", "Retry key")
	recommend := &cobra.Command{Use: "recommend", Short: "Find zero or one result from Needs or current Agent context", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		types, _ := c.Flags().GetStringSlice("types")
		ids, _ := c.Flags().GetStringSlice("needs")
		v := map[string]any{}
		if len(types) > 0 {
			v["source_kinds"] = types
		}
		if len(ids) > 0 {
			v["need_ids"] = ids
		}
		return discoveryCall(c, "POST", "/api/v2/discovery/recommendations", v, nil, true)
	}}
	recommend.Flags().StringSlice("types", nil, "Source kinds")
	recommend.Flags().StringSlice("needs", nil, "Owned active Need IDs")
	recommend.Flags().String("idempotency-key", "", "Retry key")
	taxonomy := &cobra.Command{Use: "taxonomy", Short: "Look up canonical intents"}
	lookup := &cobra.Command{Use: "search <phrase>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		cat, _ := c.Flags().GetString("category")
		sub, _ := c.Flags().GetString("subtype")
		return discoveryCall(c, "GET", "/api/v2/taxonomy/search", nil, map[string]string{"query": args[0], "category": cat, "subtype": sub}, false)
	}}
	lookup.Flags().String("category", "", "Canonical category")
	lookup.Flags().String("subtype", "", "Canonical subtype")
	taxonomy.AddCommand(lookup)
	return []*cobra.Command{search, recommend, taxonomy}
}

func newDiscoveryNeedCommands() []*cobra.Command {
	var commands []*cobra.Command
	for _, op := range []string{"create", "update", "get", "list", "pause", "resume", "close"} {
		op := op
		sub := &cobra.Command{Use: op, RunE: func(c *cobra.Command, args []string) error {
			path := "/api/v2/needs"
			if op != "create" && op != "list" {
				if err := validDiscoveryID(args[0]); err != nil {
					return err
				}
				path += "/" + args[0]
			}
			switch op {
			case "get":
				return discoveryCall(c, "GET", path, nil, nil, false)
			case "list":
				state, _ := c.Flags().GetString("state")
				cursor, _ := c.Flags().GetString("cursor")
				return discoveryCall(c, "GET", path, nil, map[string]string{"state": state, "cursor": cursor}, false)
			case "create", "update":
				file, _ := c.Flags().GetString("file")
				v, err := discoveryFile(file)
				if err != nil {
					return err
				}
				method := "POST"
				if op == "update" {
					rev, _ := c.Flags().GetInt64("revision")
					if rev <= 0 {
						return fmt.Errorf("--revision required")
					}
					v["expected_revision"] = rev
					method = "PUT"
				}
				return discoveryCall(c, method, path, v, nil, false)
			default:
				rev, _ := c.Flags().GetInt64("revision")
				if rev <= 0 {
					return fmt.Errorf("--revision required")
				}
				state := map[string]string{"pause": "paused", "resume": "active", "close": "completed"}[op]
				return discoveryCall(c, "POST", path+"/state", map[string]any{"state": state, "expected_revision": rev}, nil, false)
			}
		}}
		if op == "list" || op == "create" {
			sub.Args = cobra.NoArgs
		} else {
			sub.Use += " <id>"
			sub.Args = cobra.ExactArgs(1)
		}
		sub.Flags().String("file", "", "Need JSON file")
		sub.Flags().String("idempotency-key", "", "Create retry key")
		sub.Flags().Int64("revision", 0, "Expected current revision")
		if op == "list" {
			sub.Flags().String("state", "", "State filter")
			sub.Flags().String("cursor", "", "Continuation cursor")
		}
		commands = append(commands, sub)
	}
	return commands
}
func init() { rootCmd.AddCommand(newDiscoveryCommands()...) }
