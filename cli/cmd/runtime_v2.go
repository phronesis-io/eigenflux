package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/controlcontext"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var runtimeV2Cmd = &cobra.Command{
	Use:   "runtime",
	Short: "Maintain the Agent V2 Runtime lease and command queue",
}

var runtimeV2HeartbeatCmd = &cobra.Command{
	Use:   "heartbeat",
	Short: "Renew the short Runtime lease and reconcile pending commands",
	RunE: func(cmd *cobra.Command, _ []string) error {
		clientV2, server, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		credentials, err := auth.LoadV2Credentials(server.Name)
		if err != nil {
			return err
		}
		runtimeID, err := runtimeInstanceID(server.Name)
		if err != nil {
			return err
		}
		capabilityText, _ := cmd.Flags().GetString("capabilities")
		capabilities := make([]string, 0, 8)
		seen := map[string]struct{}{}
		for _, capability := range strings.Split(capabilityText, ",") {
			capability = strings.TrimSpace(capability)
			if capability == "" || len(capability) > 64 {
				return fmt.Errorf("--capabilities contains an invalid value")
			}
			if _, exists := seen[capability]; !exists {
				seen[capability] = struct{}{}
				capabilities = append(capabilities, capability)
			}
		}
		if len(capabilities) > 32 {
			return fmt.Errorf("--capabilities supports at most 32 values")
		}
		request := map[string]interface{}{
			"runtime_instance_id": runtimeID, "capabilities": capabilities,
		}
		if sessionRef, _ := cmd.Flags().GetString("session-ref"); sessionRef != "" {
			request["session_ref"] = sessionRef
		}
		if snapshot, cacheErr := controlcontext.Load(server.Name, credentials.AgentID); cacheErr == nil && snapshot.Revision > 0 {
			request["applied_context_revision"] = snapshot.Revision
		}
		response, err := clientV2.Post("/runtime/heartbeat", request)
		if err != nil {
			return err
		}
		output.PrintData(response.Data, resolveFormat())
		return nil
	},
}

var runtimeV2CommandCmd = &cobra.Command{Use: "command", Short: "Claim and complete owner control commands"}

var runtimeV2CommandPendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "List pending or expired-claim commands",
	RunE: func(cmd *cobra.Command, _ []string) error {
		limit, _ := cmd.Flags().GetInt("limit")
		clientV2, _, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		response, err := clientV2.Get("/agent-commands/pending", map[string]string{"limit": strconv.Itoa(limit)})
		if err != nil {
			return err
		}
		output.PrintData(response.Data, resolveFormat())
		return nil
	},
}

var runtimeV2CommandClaimCmd = &cobra.Command{
	Use:   "claim",
	Short: "Atomically claim one command using the local applied context revision",
	RunE: func(cmd *cobra.Command, _ []string) error {
		commandID, _ := cmd.Flags().GetString("command-id")
		if _, err := strconv.ParseInt(commandID, 10, 64); err != nil {
			return fmt.Errorf("--command-id is required and must be numeric")
		}
		clientV2, server, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		credentials, err := auth.LoadV2Credentials(server.Name)
		if err != nil {
			return err
		}
		snapshot, err := controlcontext.Load(server.Name, credentials.AgentID)
		if err != nil || snapshot.Revision <= 0 {
			return fmt.Errorf("no applied Agent V2 context; run 'eigenflux context pull' first")
		}
		runtimeID, err := runtimeInstanceID(server.Name)
		if err != nil {
			return err
		}
		response, err := clientV2.Post("/agent-commands/"+commandID+"/claim", map[string]interface{}{
			"runtime_instance_id": runtimeID, "applied_context_revision": snapshot.Revision,
		})
		if err != nil {
			return err
		}
		output.PrintData(response.Data, resolveFormat())
		return nil
	},
}

var runtimeV2CommandCompleteCmd = &cobra.Command{
	Use:   "complete",
	Short: "Complete a command with its current fencing proof",
	RunE: func(cmd *cobra.Command, _ []string) error {
		commandID, _ := cmd.Flags().GetString("command-id")
		claimToken, _ := cmd.Flags().GetString("claim-token")
		claimEpoch, _ := cmd.Flags().GetInt64("claim-epoch")
		status, _ := cmd.Flags().GetString("status")
		resultText, _ := cmd.Flags().GetString("result")
		if _, err := strconv.ParseInt(commandID, 10, 64); err != nil || claimToken == "" || claimEpoch <= 0 ||
			(status != "completed" && status != "failed") {
			return fmt.Errorf("command ID, claim token, positive claim epoch, and completed|failed status are required")
		}
		var result map[string]interface{}
		if json.Unmarshal([]byte(resultText), &result) != nil {
			return fmt.Errorf("--result must be a JSON object")
		}
		clientV2, server, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		runtimeID, err := runtimeInstanceID(server.Name)
		if err != nil {
			return err
		}
		response, err := clientV2.Post("/agent-commands/"+commandID+"/complete", map[string]interface{}{
			"runtime_instance_id": runtimeID, "claim_epoch": claimEpoch, "claim_token": claimToken,
			"status": status, "result": result,
		})
		if err != nil {
			return err
		}
		output.PrintData(response.Data, resolveFormat())
		return nil
	},
}

func init() {
	runtimeV2HeartbeatCmd.Flags().String("capabilities", "cli,feed,commands", "comma-separated Runtime capabilities")
	runtimeV2HeartbeatCmd.Flags().String("session-ref", "", "opaque host session reference used only by the Runtime adapter")
	runtimeV2CommandPendingCmd.Flags().Int("limit", 20, "maximum commands to return (1-50)")
	runtimeV2CommandClaimCmd.Flags().String("command-id", "", "command ID to claim")
	runtimeV2CommandCompleteCmd.Flags().String("command-id", "", "command ID to complete")
	runtimeV2CommandCompleteCmd.Flags().String("claim-token", "", "claim fencing token returned by claim")
	runtimeV2CommandCompleteCmd.Flags().Int64("claim-epoch", 0, "claim fencing epoch returned by claim")
	runtimeV2CommandCompleteCmd.Flags().String("status", "completed", "completion status: completed or failed")
	runtimeV2CommandCompleteCmd.Flags().String("result", "{}", "JSON object result")
	runtimeV2CommandCmd.AddCommand(runtimeV2CommandPendingCmd, runtimeV2CommandClaimCmd, runtimeV2CommandCompleteCmd)
	runtimeV2Cmd.AddCommand(runtimeV2HeartbeatCmd, runtimeV2CommandCmd)
	rootCmd.AddCommand(runtimeV2Cmd)
}
