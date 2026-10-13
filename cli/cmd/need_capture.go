package cmd

import (
	"fmt"
	"strconv"

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
		raw, err := readNeedJSONFile(file, 100<<10)
		if err != nil {
			return err
		}
		cli, _, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		response, err := cli.Post("/need-capture/complete", raw)
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
After a timeout retry the same file. On stale Intent, concurrent capture, or a
completion conflict, reread pending work. Never retry old content for a new version.
Maximum file size: 100 KiB. Each input remains limited to 32 KiB.`
	complete.Flags().String("file", "", "Private JSON capture review file")
	group.AddCommand(pending, complete)
	return group
}
