package cmd

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

var walletCmd = &cobra.Command{
	Use:   "wallet",
	Short: "Inspect balances, bind payment authorization, and manage withdrawals",
	Long: `Inspect balances, bind payout authorization, verify identity, and withdraw.

Payouts currently support Alipay (CNY) only. Funds leave the wallet solely to a
bound Alipay account that has passed mainland identity verification, which
requires a mainland identity-card number. Order completion, credit maturity, and
withdrawal settlement remain distinct events.`,
}

func walletReadCommand(use, short, path string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			resp, err := newCommissionClient().Get(path, nil)
			if err != nil {
				return err
			}
			return printResponse(resp)
		},
	}
}

var walletGetCmd = walletReadCommand("get", "Get wallet state", "/wallet")
var walletBalanceCmd = walletReadCommand("balance", "Get available and held balances", "/wallet/balance")

var walletBindCmd = &cobra.Command{
	Use:   "bind",
	Short: "Bind a payment authorization to the wallet",
	Long: `Bind a payout authorization to the wallet.

An Alipay account is the only bindable payout account. Binding returns a cooling
timestamp and is not proof of identity verification or payout eligibility;
verify identity with wallet kyc before expecting a withdrawal to settle.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		authorization, _ := cmd.Flags().GetString("authorization")
		if authorization == "" {
			return fmt.Errorf("--authorization is required")
		}
		body := map[string]any{"authorization": authorization}
		key, _ := cmd.Flags().GetString("idempotency-key")
		resp, err := postMutation(newCommissionClient(), "/wallet/binding", "wallet.bind", key, body)
		if err != nil {
			return err
		}
		return printResponse(resp)
	},
}

var walletWithdrawCmd = &cobra.Command{
	Use:   "withdraw",
	Short: "Create a withdrawal request",
	Long: `Create a withdrawal request for withdrawable funds.

Withdrawals are paid to the bound Alipay account only, after that account passes
mainland identity verification. Alipay (CNY) is currently the only supported
payout channel. An accepted request is not settled payment; read its exact state.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		amount, _ := cmd.Flags().GetInt64("amount-fen")
		if amount <= 0 {
			return fmt.Errorf("--amount-fen must be positive")
		}
		body := map[string]any{"amount_fen": amount}
		key, _ := cmd.Flags().GetString("idempotency-key")
		resp, err := postMutation(newCommissionClient(), "/wallet/withdrawals", "wallet.withdraw", key, body)
		if err != nil {
			return err
		}
		return printResponse(resp)
	},
}

var walletWithdrawalsCmd = &cobra.Command{
	Use:   "withdrawals",
	Short: "List withdrawals with their exact settlement states",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cursor, _ := cmd.Flags().GetInt64("cursor")
		limit, _ := cmd.Flags().GetInt("limit")
		params := map[string]string{"limit": strconv.Itoa(limit)}
		if cursor > 0 {
			params["cursor"] = strconv.FormatInt(cursor, 10)
		}
		resp, err := newCommissionClient().Get("/wallet/withdrawals", params)
		if err != nil {
			return err
		}
		return printResponse(resp)
	},
}

var walletWithdrawalCmd = &cobra.Command{
	Use:   "withdrawal <withdrawal-id>",
	Short: "Get a withdrawal and its exact settlement state",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		id, err := numericArgument(args, "withdrawal ID")
		if err != nil {
			return err
		}
		resp, err := newCommissionClient().Get("/wallet/withdrawals/"+strconv.FormatInt(id, 10), nil)
		if err != nil {
			return err
		}
		return printResponse(resp)
	},
}

func init() {
	walletBindCmd.Flags().String("authorization", "", "provider authorization payload")
	addIdempotencyFlag(walletBindCmd)
	walletWithdrawCmd.Flags().Int64("amount-fen", 0, "withdrawal amount in minor currency units")
	addIdempotencyFlag(walletWithdrawCmd)
	walletWithdrawalsCmd.Flags().Int64("cursor", 0, "pagination cursor")
	walletWithdrawalsCmd.Flags().Int("limit", 20, "maximum withdrawals")
	walletCmd.AddCommand(walletGetCmd, walletBalanceCmd, walletBindCmd, walletWithdrawCmd, walletWithdrawalsCmd, walletWithdrawalCmd)
	rootCmd.AddCommand(walletCmd)
}
