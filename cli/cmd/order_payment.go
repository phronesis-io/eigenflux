package cmd

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

func newOrderPaymentCommand() *cobra.Command {
	command := &cobra.Command{
		Use:     "payment <order-id>",
		Aliases: []string{"pay"},
		Short:   "Get an Alipay payment link for the buyer to open and confirm",
		Long:    "Get a payment link for an existing pending-payment order. This command does not charge the buyer, open a browser, or confirm payment. The buyer completes payment in Alipay; use order get to check the resulting order state.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := numericArgument(args, "order ID")
			if err != nil {
				return err
			}
			channel, _ := cmd.Flags().GetString("channel")
			if channel != "page" && channel != "wap" {
				return fmt.Errorf("--channel must be page or wap")
			}
			orderID := strconv.FormatInt(id, 10)
			body := map[string]any{"order_id": orderID, "channel": channel}
			key, _ := cmd.Flags().GetString("idempotency-key")
			response, err := postMutation(newCommissionClient(), "/orders/"+orderID+"/payment", "order.payment."+orderID, key, body)
			if err != nil {
				return err
			}
			return printResponse(response)
		},
	}
	command.Flags().String("channel", "page", "Alipay channel: page (desktop) or wap (mobile)")
	addIdempotencyFlag(command)
	return command
}

func init() {
	orderCmd.AddCommand(newOrderPaymentCommand())
}
