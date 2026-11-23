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
		Short:   "Read the payment action for this Order",
		Long:    "Read the action for the frozen Order. Wallet-funded Orders use balance and can require Console confirmation or an Alipay top-up. Existing Alipay Orders return their original payment link. The buyer opens the link. Use order get to check payment.",
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
