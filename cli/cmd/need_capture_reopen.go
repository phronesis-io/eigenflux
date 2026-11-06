package cmd

import (
	"fmt"
	"math"
	"strconv"

	"github.com/spf13/cobra"
)

func newNeedCaptureReviewCommand(reopen bool) *cobra.Command {
	name, short := "review", "Inspect the server's review for one current Intent version"
	if reopen {
		name, short = "reopen", "Requeue one explicitly selected no_need review without editing its Intent"
	}
	c := &cobra.Command{Use: name, Short: short, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		intent, _ := c.Flags().GetString("intent-id")
		version, _ := c.Flags().GetInt64("intent-version")
		if err := validateNeedID(intent); err != nil {
			return err
		}
		if version <= 0 {
			return fmt.Errorf("--intent-version must be positive")
		}
		revision := int64(0)
		if reopen {
			revision, _ = c.Flags().GetInt64("expected-capture-revision")
			if !c.Flags().Changed("expected-capture-revision") || revision < 0 || revision == math.MaxInt64 {
				return fmt.Errorf("--expected-capture-revision must explicitly match the inspected review")
			}
		}
		cli, _, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		if reopen {
			response, err := cli.Post("/need-capture/reopen", map[string]any{"intent_id": intent, "intent_version": version, "expected_capture_revision": revision})
			return printNeedResponse(response, err)
		}
		response, err := cli.Get("/need-capture/review", map[string]string{"intent_id": intent, "intent_version": strconv.FormatInt(version, 10)})
		return printNeedResponse(response, err)
	}}
	c.Flags().String("intent-id", "", "Exact current Intent ID")
	c.Flags().Int64("intent-version", 0, "Exact current Intent version")
	if reopen {
		c.Flags().Int64("expected-capture-revision", 0, "Revision returned by need capture review; required even when zero")
		c.Long = `Reopen one current completed no_need review after an explicit selection.
Read need capture review first and copy its capture_revision. Reuse all arguments
on retry: a repeated reopen never resets a later completion. Captured reviews,
inactive/foreign Intents and stale versions cannot be reopened. This command does
not extract Needs, change the Intent, or bypass normal Pending limits. A later
full heartbeat processes the selected version with the current signed Skills.
Never run this automatically because a heartbeat has no Pending work.`
	}
	return c
}
