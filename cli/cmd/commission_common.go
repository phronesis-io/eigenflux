package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/idempotency"
	"cli.eigenflux.ai/internal/output"
)

const idempotencyHeader = "Idempotency-Key"

func mutationKey(explicit, operation string, body any) (string, error) {
	return idempotency.Key(explicit, activeAgentScope(), operation, body)
}

func postMutation(c *client.Client, path, operation, explicit string, body any) (*client.APIResponse, error) {
	key, err := mutationKey(explicit, operation, body)
	if err != nil {
		return nil, err
	}
	return c.PostWithHeaders(path, body, map[string]string{idempotencyHeader: key})
}

func putMutation(c *client.Client, path, operation, explicit string, body any) (*client.APIResponse, error) {
	key, err := mutationKey(explicit, operation, body)
	if err != nil {
		return nil, err
	}
	return c.PutWithHeaders(path, body, map[string]string{idempotencyHeader: key})
}

func printResponse(resp *client.APIResponse) error {
	if resp.Code != 0 {
		return fmt.Errorf("%s", resp.Msg)
	}
	if resolveFormat() != "table" {
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	}
	return printTableJSON(resp.Data)
}

func printTableJSON(raw json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	printTableValue("", value, 0)
	return nil
}

func printTableValue(label string, value any, depth int) {
	indent := strings.Repeat("  ", depth)
	switch typed := value.(type) {
	case map[string]any:
		if label != "" {
			fmt.Printf("%s%s\n", indent, strings.ToUpper(label))
			depth++
			indent = strings.Repeat("  ", depth)
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			switch typed[key].(type) {
			case map[string]any, []any:
				printTableValue(key, typed[key], depth)
			default:
				fmt.Printf("%s%-28s %v\n", indent, strings.ToUpper(key), typed[key])
			}
		}
	case []any:
		if label != "" {
			fmt.Printf("%s%s (%d)\n", indent, strings.ToUpper(label), len(typed))
		}
		for index, item := range typed {
			fmt.Printf("%s[%d]\n", indent, index+1)
			printTableValue("", item, depth+1)
		}
	default:
		fmt.Printf("%s%-28s %v\n", indent, strings.ToUpper(label), typed)
	}
}

func commaList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
