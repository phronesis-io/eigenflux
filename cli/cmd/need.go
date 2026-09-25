package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

func readNeedFile(path string) (json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (32<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > 32<<10 || !json.Valid(raw) {
		return nil, fmt.Errorf("Need file must contain one JSON object of at most 32 KiB")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, fmt.Errorf("Need must be a JSON object")
	}
	return raw, nil
}
func validateNeedID(raw string) error {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return fmt.Errorf("Need ID must be a positive decimal int64")
	}
	return nil
}
func newNeedCommand() *cobra.Command {
	root := &cobra.Command{Use: "need", Short: "Prepare structured Needs for confirmed intents"}
	group := &cobra.Command{Use: "input", Short: "Save and inspect immutable NeedInputs"}
	root.AddCommand(group)
	create := &cobra.Command{Use: "create --file need.json --idempotency-key KEY", Short: "Save an Agent interpretation of a confirmed intent version", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		file, _ := c.Flags().GetString("file")
		key, _ := c.Flags().GetString("idempotency-key")
		if len(key) < 8 || len(key) > 128 {
			return fmt.Errorf("--idempotency-key must contain 8 to 128 printable ASCII characters; reuse it for retries")
		}
		for _, r := range key {
			if r < 33 || r > 126 {
				return fmt.Errorf("--idempotency-key must contain printable ASCII without spaces")
			}
		}
		raw, err := readNeedFile(file)
		if err != nil {
			return err
		}
		cli, _, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		response, err := cli.PostWithHeaders("/need-inputs", raw, map[string]string{"Idempotency-Key": key})
		return printNeedResponse(response, err)
	}}
	create.Long = `Save an Agent-authored need_input.v2 linked to an active confirmed Intent.
Read the current source with eigenflux context intent list before filling the file.

Required fields:
  schema_version   "need_input.v2"
  intent_id        Confirmed Intent ID as a positive decimal string.
  intent_version   Exact positive integer version of that Intent.
  need_type        broadcast: seek information; agent: find an Agent;
                   commission: seek a service to commission.
  target.goal      Desired outcome, not just a topic (200 weighted characters).

Optional fields:
  target.context   Necessary background, not the full conversation (2000).
  constraints      JSON object of explicit measurable mandatory restrictions.
  requirements     Array of mandatory open conditions; at most 20 objects.
  preferences      Array of optional ranking preferences; never hard filters.
                   At most 20 objects.
  priority         Source-supported value from 0 to 1; omit when unclear.
                   Do not copy the Intent's different priority scale.

Each requirement or preference has required text (500 weighted characters)
and optional source_quote (1000). Copy supporting user wording when available;
a source quote is interpretation provenance, not verified candidate evidence.

Constraint fields:
  budget_max_fen              Maximum budget in integer CNY fen, at least zero.
                             Requires currency.
  currency                   "CNY" only.
  max_promised_delivery_ms    Maximum promised delivery duration in milliseconds,
                             an integer of at least zero.
  deadline_ms                Absolute deadline as a positive Unix millisecond integer.
  provider_region            Array of ISO two-letter country codes.
  lang                       Array of BCP 47 language codes.
  exclude_terms              Array of explicitly excluded terms.
Budget, currency and promised delivery duration are commission-only.
Constraint arrays allow at most 20 nonblank values of 100 weighted characters each.

CJK characters count as 2, other characters as 1. The JSON body limit is 32 KiB.
Omit unstated or unknown values; do not guess or replace them with zero. Keep
optional language, region, price and timing preferences in preferences.
Do not submit candidate phrases, canonical taxonomy IDs or normalized results.

The CLI submits the original JSON; the server validates and stores its snapshot.
New records have status active and eligible on the NeedInput record. Eligibility
means the linked Intent is current and active, not that candidate conditions have
been verified. Capture does not run search or create a subscription.

Reuse an idempotency key for identical retries. Use a new key for a changed input
or Intent version. On INTENT_REVISION_STALE, reread the Intent before resubmitting.`
	create.Flags().String("file", "", "NeedInput JSON file (need_input.v2)")
	create.Flags().String("idempotency-key", "", "Stable retry key for this intent interpretation")
	group.AddCommand(create)
	group.AddCommand(&cobra.Command{Use: "get <need-input-id>", Short: "Read an owned NeedInput", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		if err := validateNeedID(args[0]); err != nil {
			return err
		}
		cli, _, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		response, err := cli.Get("/need-inputs/"+args[0], nil)
		return printNeedResponse(response, err)
	}})
	list := &cobra.Command{Use: "list", Short: "List owned NeedInputs, newest first", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		cursor, _ := c.Flags().GetString("cursor")
		if cursor != "" {
			if err := validateNeedID(cursor); err != nil {
				return err
			}
		}
		limit, _ := c.Flags().GetInt("limit")
		if limit < 1 || limit > 100 {
			return fmt.Errorf("--limit must be between 1 and 100")
		}
		cli, _, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		response, err := cli.Get("/need-inputs", map[string]string{"cursor": cursor, "limit": strconv.Itoa(limit)})
		return printNeedResponse(response, err)
	}}
	list.Flags().String("cursor", "", "Continuation cursor from the previous page")
	list.Flags().Int("limit", 20, "Maximum records (1..100)")
	group.AddCommand(list)
	return root
}
func printNeedResponse(response *client.APIResponse, err error) error {
	if err != nil {
		return err
	}
	output.PrintData(response.Data, resolveFormat())
	return nil
}
func init() { rootCmd.AddCommand(newNeedCommand()) }
