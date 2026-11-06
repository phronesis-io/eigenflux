package cmd

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newOrderWithdrawInputRequestCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "withdraw-input-request <order-id>",
		Short: "Withdraw the seller's pending clarification and wake fresh input inspection",
		Long:  "Withdraw a pending seller clarification using its exact request ID and Order version. Preserve buyer inputs and the frozen contract. The service wakes a fresh seller input check; withdrawal does not accept the Order or establish readiness.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := numericArgument(args, "order ID")
			if err != nil {
				return err
			}
			version, _ := cmd.Flags().GetInt64("expected-version")
			if version <= 0 {
				return errors.New("--expected-version must be positive")
			}
			requestID, _ := cmd.Flags().GetString("request-id")
			decoded, err := hex.DecodeString(requestID)
			if err != nil || len(decoded) != 32 || strings.ToLower(requestID) != requestID {
				return errors.New("--request-id must be the exact 64-character lowercase hexadecimal request ID")
			}
			reason, _ := cmd.Flags().GetString("reason")
			if !safeClarificationText(reason, 2000) {
				return errors.New("--reason must be valid UTF-8 text of at most 2000 bytes")
			}
			identity, err := watchBindingIdentity()
			if err != nil {
				return err
			}
			body := map[string]any{"expected_version": version, "request_id": requestID}
			if reason != "" {
				body["reason"] = reason
			}
			key, _ := cmd.Flags().GetString("idempotency-key")
			response, err := postMutation(newCommissionClient(), "/orders/"+strconv.FormatInt(id, 10)+"/withdraw-input-request", "order.withdraw-input-request."+strconv.FormatInt(id, 10), key, body)
			if err != nil {
				return err
			}
			if response.Code != 0 {
				return fmt.Errorf("%s", response.Msg)
			}
			var data struct {
				Order commissionIntakeOrder `json:"order"`
			}
			if json.Unmarshal(response.Data, &data) != nil || int64(data.Order.OrderID) != id || strconv.FormatInt(int64(data.Order.SellerID), 10) != identity.AgentID || data.Order.Version != version+1 || data.Order.State != "awaiting_seller" || !validCommissionInputRequest(data.Order.InputRequest) || data.Order.InputRequest.RequestID != requestID || data.Order.InputRequest.Status != "withdrawn" {
				return errors.New("commission_withdraw_receipt_invalid: inspect the current Order before retrying")
			}
			return printResponse(response)
		},
	}
	command.Flags().Int64("expected-version", 0, "current Order version")
	command.Flags().String("request-id", "", "exact pending clarification request ID")
	command.Flags().String("reason", "", "why the seller no longer needs clarification")
	addIdempotencyFlag(command)
	return command
}

func init() { orderCmd.AddCommand(newOrderWithdrawInputRequestCommand()) }
