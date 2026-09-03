package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var attentionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Agent Attention items awaiting or recording human decisions",
	RunE: func(cmd *cobra.Command, _ []string) error {
		status, _ := cmd.Flags().GetString("status")
		limit, _ := cmd.Flags().GetInt("limit")
		cursor, _ := cmd.Flags().GetString("cursor")
		if limit < 1 || limit > 50 {
			return fmt.Errorf("--limit must be between 1 and 50")
		}
		clientV2, _, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		response, err := clientV2.Get("/agent-attention-items", map[string]string{
			"status": status, "limit": strconv.Itoa(limit), "cursor": cursor,
		})
		if err != nil {
			return err
		}
		output.PrintData(json.RawMessage(response.Data), resolveFormat())
		return nil
	},
}

var attentionRespondCmd = &cobra.Command{
	Use:   "respond",
	Short: "Apply one explicit human selection to an open Attention item",
	RunE: func(cmd *cobra.Command, _ []string) error {
		attentionID, _ := cmd.Flags().GetString("attention-id")
		actionKey, _ := cmd.Flags().GetString("action-key")
		expectedRevision, _ := cmd.Flags().GetInt64("expected-revision")
		if parsed, err := strconv.ParseInt(attentionID, 10, 64); err != nil || parsed <= 0 {
			return fmt.Errorf("--attention-id must be a positive integer")
		}
		if strings.TrimSpace(actionKey) == "" || expectedRevision <= 0 {
			return fmt.Errorf("--action-key and a positive --expected-revision are required")
		}
		key, err := contextMutationKey("cli-attention-response")
		if err != nil {
			return err
		}
		clientV2, _, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		response, err := clientV2.Post("/agent-attention-items/"+attentionID+"/respond", map[string]interface{}{
			"action_key": actionKey, "expected_item_revision": expectedRevision, "idempotency_key": key,
		})
		if err != nil {
			return err
		}
		output.PrintData(json.RawMessage(response.Data), resolveFormat())
		return nil
	},
}

var attentionDismissCmd = &cobra.Command{
	Use:   "dismiss",
	Short: "Dismiss one open Attention item on explicit human instruction",
	RunE: func(cmd *cobra.Command, _ []string) error {
		attentionID, _ := cmd.Flags().GetString("attention-id")
		expectedRevision, _ := cmd.Flags().GetInt64("expected-revision")
		if parsed, err := strconv.ParseInt(attentionID, 10, 64); err != nil || parsed <= 0 {
			return fmt.Errorf("--attention-id must be a positive integer")
		}
		if expectedRevision <= 0 {
			return fmt.Errorf("a positive --expected-revision from a fresh attention list is required")
		}
		clientV2, _, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		response, err := clientV2.Post("/agent-attention-items/"+attentionID+"/dismiss", map[string]interface{}{
			"expected_item_revision": expectedRevision,
		})
		if err != nil {
			return err
		}
		output.PrintData(json.RawMessage(response.Data), resolveFormat())
		return nil
	},
}

func init() {
	attentionListCmd.Flags().String("status", "open", "open|selected|pending|acted|dismissed|expired")
	attentionListCmd.Flags().Int("limit", 20, "maximum number of items (1-50)")
	attentionListCmd.Flags().String("cursor", "", "pagination cursor")
	attentionRespondCmd.Flags().String("attention-id", "", "Attention item ID")
	attentionRespondCmd.Flags().String("action-key", "", "exact action_key from the frozen Attention item")
	attentionRespondCmd.Flags().Int64("expected-revision", 0, "item_revision from a fresh attention list")
	attentionDismissCmd.Flags().String("attention-id", "", "Attention item ID")
	attentionDismissCmd.Flags().Int64("expected-revision", 0, "item_revision from a fresh attention list")
	attentionCmd.AddCommand(attentionListCmd, attentionRespondCmd, attentionDismissCmd)
}
