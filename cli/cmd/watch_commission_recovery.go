package cmd

import (
	"encoding/json"
	"errors"
	"strconv"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	"github.com/spf13/cobra"
)

func commissionSubscription(b dispatch.Binding) string {
	if b.Handles("commission_order") {
		return "enabled_execution_unverified"
	}
	return "disabled_orders_not_consumed"
}

func validateWatchRetryArgs(cmd *cobra.Command, args []string) error {
	id, _ := cmd.Flags().GetString("order-id")
	if id == "" && len(args) == 1 {
		return nil
	}
	if id != "" && len(args) == 0 {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil && n > 0 {
			return nil
		}
	}
	return errors.New("provide exactly one JOB_ID or --order-id positive ORDER_ID")
}

func recoverWatchCommission(cmd *cobra.Command, b dispatch.Binding, q *dispatch.Journal, id string) error {
	if !b.Handles("commission_order") {
		return errors.New("commission_event_not_subscribed: bind commission_order before recovery")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	srv, err := cfg.GetActive(b.Server)
	if err != nil {
		return err
	}
	cred, err := auth.LoadV2Credentials(b.Server)
	if err != nil {
		return err
	}
	w := &accountWatch{home: b.Home, server: *srv, identity: *cred, scope: b.Scope, binding: &b, journal: q}
	order, err := w.readCommissionOrder(cmd.Context(), id)
	if err != nil {
		return err
	}
	if order.State != "awaiting_seller" && order.State != "in_progress" {
		return errors.New("commission_order_not_recoverable")
	}
	var jobID string
	err = auth.WithV2CredentialsLockContext(cmd.Context(), b.Server, commissionIntakeTimeout, func() error {
		if err := w.commissionIdentityOK(); err != nil {
			return err
		}
		var saveErr error
		jobID, saveErr = q.RecoverCommission(id, order.Version)
		return saveErr
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"status": "intake_recorded", "job_id": jobID, "order_id": id, "source": "operator_recovery", "execution": "start watch --dispatch to process; existing jobs are not replayed"})
}
