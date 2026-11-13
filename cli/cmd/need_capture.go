package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

func newNeedCaptureCommand() *cobra.Command {
	group := &cobra.Command{Use: "capture", Short: "Maintain internal Need capture for current confirmed Intents"}
	pending := &cobra.Command{Use: "pending", Short: "List current Intent versions awaiting Agent review", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		limit, _ := c.Flags().GetInt("limit")
		if limit < 1 || limit > 10 {
			return fmt.Errorf("--limit must be between 1 and 10")
		}
		cli, _, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		response, err := cli.Get("/need-capture/pending", map[string]string{"limit": strconv.Itoa(limit)})
		return printNeedResponse(response, err)
	}}
	pending.Flags().Int("limit", 2, "Maximum Intent versions to review in this cycle (1..10)")
	complete := &cobra.Command{Use: "complete --file review.json", Short: "Save missing NeedInputs and complete one Intent version review", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		file, _ := c.Flags().GetString("file")
		plan, _ := c.Flags().GetString("plan")
		raw, err := readNeedJSONFile(file, 100<<10)
		if err != nil {
			return err
		}
		var scope maintenance.Scope
		var cli *client.Client
		if plan != "" {
			scope, err = maintenanceScope(activeServerName())
			if err == nil {
				cli, err = newNeedCaptureClient(scope, clientMetaForServerName(scope.Server), needCaptureSubmitTimeout)
			}
		} else {
			cli, _, err = newV2ClientForServer(serverFlag, true)
		}
		if err != nil {
			return err
		}
		response, err := submitNeedCapture(cli, raw, scope, plan)
		return printNeedResponse(response, err)
	}}
	complete.Long = `Complete one current confirmed Intent version without changing the Intent or action policy.
The JSON object contains intent_id (decimal string), intent_version (integer),
outcome (captured or no_need), inputs (up to three need_input.v2 objects, one per
missing type), and reason (required for no_need, at most 2000 UTF-8 bytes).
Inspect pending.existing_inputs and reuse existing captures; do not resubmit their
kinds. captured accepts an empty inputs array when existing captures suffice.
no_need requires no inputs and no existing captures. Preserve all supported types
of the source Intent; never invent a Need to finish the review.
The server atomically saves inputs and completion. Identical retries are safe.
After a timeout retry the same file once. On stale Intent, concurrent capture, or a
completion conflict, leave that version for the next heartbeat's Pending selection.
Never retry old content for a new version. --plan attaches local submission receipts
for a version selected by heartbeat plan; status reads these receipts without a
new server query. Unconfirmed work never suppresses otherwise completed Feed output.
Maximum file size: 100 KiB. Each input remains limited to 32 KiB.`
	complete.Flags().String("file", "", "Private JSON capture review file")
	complete.Flags().String("plan", "", "Heartbeat Need plan ID for local submission receipts")
	status := &cobra.Command{Use: "status --plan <id>", Short: "Read local Need submission receipts without querying or changing the server", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		plan, _ := c.Flags().GetString("plan")
		scope, err := maintenanceScope(activeServerName())
		if err != nil {
			return err
		}
		run, err := maintenance.ReadNeedCaptureRun(scope, plan)
		if err != nil {
			return err
		}
		confirmed := run.QueryStatus != "failed"
		for _, item := range run.Items {
			confirmed = confirmed && item.Status == "confirmed"
		}
		output.PrintData(struct {
			maintenance.NeedCaptureRun
			AllConfirmed bool   `json:"all_confirmed"`
			Evidence     string `json:"evidence"`
		}{run, confirmed, "local_submission_receipts"}, resolveFormat())
		return nil
	}}
	status.Flags().String("plan", "", "Heartbeat Need plan ID")
	failure := &cobra.Command{Use: "failure --plan <id> --intent-id <id> --intent-version <version>", Short: "Record an actual extraction failure for a selected Intent", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		plan, _ := c.Flags().GetString("plan")
		id, _ := c.Flags().GetString("intent-id")
		version, _ := c.Flags().GetInt64("intent-version")
		scope, err := maintenanceScope(activeServerName())
		if err != nil {
			return err
		}
		return maintenance.RecordNeedCaptureResult(scope, plan, id, version, "extraction_failed", "", "")
	}}
	failure.Flags().String("plan", "", "Heartbeat Need plan ID")
	failure.Flags().String("intent-id", "", "Selected Intent ID")
	failure.Flags().Int64("intent-version", 0, "Selected Intent version")
	group.AddCommand(pending, complete, status, failure)
	return group
}

func submitNeedCapture(cli *client.Client, raw json.RawMessage, scope maintenance.Scope, plan string) (*client.APIResponse, error) {
	var review struct {
		IntentID      string `json:"intent_id"`
		IntentVersion int64  `json:"intent_version"`
		Outcome       string `json:"outcome"`
	}
	receiptUnavailable := false
	if plan != "" {
		if json.Unmarshal(raw, &review) != nil {
			return nil, fmt.Errorf("invalid capture review")
		}
		// Persist uncertainty before sending. A killed process or lost response
		// cannot manufacture a success receipt; the next server Pending query decides retries.
		if err := maintenance.RecordNeedCaptureResult(scope, plan, review.IntentID, review.IntentVersion, "unconfirmed", review.Outcome, ""); err != nil {
			if errors.Is(err, maintenance.ErrNeedCaptureSelection) {
				return nil, err
			}
			// Local bookkeeping is not the authority for ownership or Intent
			// versions. Preserve the original payload and let the server validate
			// it even when the receipt is missing, expired or unwritable.
			receiptUnavailable = true
		}
	}
	response, err := cli.Post("/need-capture/complete", raw)
	status, code := "confirmed", ""
	if err != nil {
		status, code = "unconfirmed", needCaptureErrorCode(err)
		var api *client.APIError
		if errors.As(err, &api) && api.StatusCode >= 400 && api.StatusCode < 500 {
			status = "failed"
		}
	} else {
		var receipt struct {
			Completed bool `json:"completed"`
		}
		if response == nil || response.Code != 0 || json.Unmarshal(response.Data, &receipt) != nil || !receipt.Completed {
			status, code = "unconfirmed", "need_response_invalid"
			err = fmt.Errorf("Need capture completion was not confirmed")
		}
	}
	if plan != "" {
		if receiptErr := maintenance.RecordNeedCaptureResult(scope, plan, review.IntentID, review.IntentVersion, status, review.Outcome, code); receiptErr != nil {
			receiptUnavailable = true
		}
		if receiptUnavailable && err == nil {
			// Preserve the successful server response even if local persistence fails.
			var data map[string]json.RawMessage
			if json.Unmarshal(response.Data, &data) == nil && data != nil {
				data["local_receipt_error"] = json.RawMessage(`"need_receipt_unavailable"`)
				response.Data, _ = json.Marshal(data)
			}
		}
	}
	return response, err
}
