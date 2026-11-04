package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/spf13/cobra"
)

func acceptOrderAfterInspection(cmd *cobra.Command, orderID, version int64, explicitKey string, body map[string]any) error {
	expected, err := watchBindingIdentity()
	if err != nil {
		return err
	}
	binding, err := dispatch.ReadBinding(expected.Home, expected.Server)
	if err != nil {
		return fmt.Errorf("seller acceptance requires a bound input-check Agent: %w", err)
	}
	if !sameBindingIdentity(expected, binding) {
		return errWatchIdentity
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	if !binding.Handles("commission_order") {
		return errors.New("commission_event_not_subscribed")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	server, err := cfg.GetActive(binding.Server)
	if err != nil {
		return err
	}
	cred, err := auth.LoadV2Credentials(binding.Server)
	if err != nil {
		return err
	}
	if cred.AgentID != binding.AgentID || cred.PrincipalID != binding.PrincipalID || server.Endpoint != binding.Endpoint {
		return errWatchIdentity
	}
	w := &accountWatch{manualCommission: true, home: binding.Home, scope: binding.Scope, server: *server, identity: *cred, binding: &binding, runAgent: (dispatch.Runner{Binding: binding}).Run, emit: func(string, interface{}) error { return nil }}
	ctx, cancel := context.WithTimeout(cmd.Context(), commissionIntakeTimeout)
	defer cancel()
	id := strconv.FormatInt(orderID, 10)
	release, lockErr := watchstate.Acquire(binding.Home, binding.Server)
	if lockErr == nil {
		defer release()
	}
	// Lock failure permits only an immutable read. OpenJournal would convert the
	// active owner's running jobs to unknown and must never be used here.
	source, ready, err := dispatch.ReadCommissionReadiness(binding, id, version)
	if err != nil {
		return err
	}
	var response *client.APIResponse
	var acceptErr error
	accept := func(ctx context.Context, order commissionIntakeOrder) error {
		response, acceptErr = w.acceptManualCommission(ctx, id, version, explicitKey, body)
		return acceptErr
	}
	if ready {
		if lockErr != nil && (source.Status == "running" || source.Status == "sending") {
			return errors.New("commission_intake_in_progress: let the existing watch finish input inspection")
		}
		if err := accept(ctx, commissionIntakeOrder{}); err != nil {
			return err
		}
		return printResponse(response)
	}
	if lockErr != nil {
		return errors.New("commission_readiness_required: let the existing watch complete the real input check before accepting")
	}
	order, err := w.readCommissionOrder(ctx, id)
	if err != nil {
		return err
	}
	if order.State != "awaiting_seller" || order.Version != version {
		return errors.New("commission_order_changed")
	}
	q, err := dispatch.OpenJournal(binding)
	if err != nil {
		return err
	}
	w.journal = q
	job, err := q.ClaimCommissionAcceptance(id, version)
	if err != nil {
		return err
	}
	if err := w.dispatchCommissionIntakeWithAcceptance(ctx, job, version, accept); err != nil {
		return err
	}
	if acceptErr != nil {
		return acceptErr
	}
	if response == nil {
		for _, saved := range q.Snapshot() {
			if saved.ID == job.ID {
				return fmt.Errorf("acceptance blocked: %s", saved.Code)
			}
		}
		return errors.New("commission_readiness_required")
	}
	return printResponse(response)
}

func (w *accountWatch) acceptManualCommission(ctx context.Context, id string, version int64, explicitKey string, body map[string]any) (*client.APIResponse, error) {
	source, ready, err := dispatch.ReadCommissionReadiness(*w.binding, id, version)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, errors.New("commission_readiness_required")
	}
	order, err := w.readCommissionOrder(ctx, id)
	if err != nil {
		return nil, err
	}
	// Pending payment permits only replay of the original version-fenced request;
	// the server's same-body/key idempotency decides whether a receipt is reusable.
	if !((order.State == "awaiting_seller" && order.Version == version) || (order.State == "pending_payment" && order.Version == version+1)) {
		return nil, errors.New("commission_order_changed")
	}
	directory, err := os.MkdirTemp(w.binding.WorkDir, ".eigenflux-accept-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	files, err := w.prepareCommissionMaterials(ctx, order, directory)
	if err != nil {
		return nil, err
	}
	fingerprint, err := commissionInputFingerprint(order, files)
	if err != nil || fingerprint != source.CommissionReadiness.InputFingerprint {
		return nil, errors.New("commission_fulfillment_inputs_changed")
	}
	current, err := w.readCommissionOrder(ctx, id)
	if err != nil {
		return nil, err
	}
	if !sameCommissionIntakeOrder(order, current) || order.ExecutionRevision != current.ExecutionRevision {
		return nil, errors.New("commission_order_changed")
	}
	// Confirm that the durable proof still exists after the downloads, including
	// when its owner is concurrently processing notifications.
	retained, ok, err := dispatch.ReadCommissionReadiness(*w.binding, id, version)
	if err != nil {
		return nil, err
	}
	if !ok || retained.ID != source.ID || retained.CommissionReadiness.InputFingerprint != fingerprint {
		return nil, errors.New("commission_readiness_required")
	}
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return nil, err
	}
	key, err := mutationKey(explicitKey, "order.accept."+id, body)
	if err != nil {
		return nil, err
	}
	var response *client.APIResponse
	err = auth.WithV2CredentialsLockContext(ctx, w.server.Name, 35*time.Second, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.commissionIdentityOK(); err != nil {
			return err
		}
		credentials, err := auth.LoadV2Credentials(w.server.Name)
		if err != nil {
			return err
		}
		api.Token, api.OnUnauthorized = credentials.AccessToken, nil
		response, err = api.PostWithHeaders("/orders/"+id+"/accept", body, map[string]string{idempotencyHeader: key})
		if err != nil {
			return err
		}
		if response.Code != 0 {
			return errors.New(response.Msg)
		}
		var data struct {
			Order commissionIntakeOrder `json:"order"`
		}
		if json.Unmarshal(response.Data, &data) != nil || data.Order.OrderID != order.OrderID || data.Order.SellerID != order.SellerID || data.Order.BuyerID != order.BuyerID || data.Order.Version != version+1 || data.Order.State != "pending_payment" {
			return errors.New("commission_accept_receipt_invalid")
		}
		return nil
	})
	return response, err
}
