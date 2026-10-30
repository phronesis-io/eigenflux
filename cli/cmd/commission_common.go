package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/idempotency"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

const idempotencyHeader = "Idempotency-Key"

// fundingChannelNotice states the only payment and payout channel the platform
// currently supports, matching the Commission, Order, and Wallet help text.
// Identity comparison belongs to the payout step, not to this notice: a normal
// Alipay account already satisfies it, so front-loading it would read as an
// extra qualification the seller has to go and obtain.
const fundingChannelNotice = "Payments and payouts currently support Alipay (CNY) only. Buyers pay each Order through Alipay; sellers are paid to a bound Alipay account."

// fundingNoticeStampKey records, per server profile, that the notice was shown.
// It carries the `_` prefix every CLI-private key uses, so settings sync can
// neither export nor silence it.
const fundingNoticeStampKey = "_commission_funding_notice_at"

// discloseFundingChannel writes the funding-channel limit to stderr the first
// time this server profile reaches a command that commits preparatory work.
// Repeating it on every listing or Order would train users to ignore it, and a
// seller who already funds through Alipay has nothing to learn from it; the
// command help keeps the full statement available afterwards. stdout output
// contracts are unchanged either way.
func discloseFundingChannel(command *cobra.Command) *cobra.Command {
	wrapped := command.RunE
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if claimFundingNotice(activeServerName(), time.Now().Unix()) {
			fmt.Fprintln(os.Stderr, "Notice: "+fundingChannelNotice)
		}
		return wrapped(cmd, args)
	}
	return command
}

// claimFundingNotice reports whether this server profile still needs the notice
// and stamps it. Best-effort: a configuration or storage error discloses the
// limit rather than hiding it, and an unresolved server name always discloses
// because no stamp can be scoped to it.
func claimFundingNotice(serverName string, now int64) bool {
	if serverName == "" || now <= 0 {
		return true
	}
	cfg, err := config.Load()
	if err != nil {
		return true
	}
	if stamp, ok, err := cfg.GetServerOnlyKV(serverName, fundingNoticeStampKey); err == nil && ok {
		if parsed, err := strconv.ParseInt(stamp, 10, 64); err == nil && parsed > 0 && parsed <= now {
			return false
		}
	}
	_ = cfg.SetServerKV(serverName, fundingNoticeStampKey, strconv.FormatInt(now, 10))
	return true
}

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
