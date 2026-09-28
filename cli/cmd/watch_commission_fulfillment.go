package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
)

// Fulfillment has its own serial lane and host timeout, keeping long local work
// off both PM delivery and the short seller-inspection budget.
func (w *accountWatch) commissionFulfillmentLoop(ctx context.Context) error {
	if !w.receivesCommission() {
		return nil
	}
	for ctx.Err() == nil {
		job, ok, err := w.journal.NextKind("commission_fulfillment")
		if err != nil {
			return err
		}
		if !ok {
			if !watchPause(ctx, time.Second) {
				break
			}
			continue
		}
		if err := w.dispatchCommissionFulfillment(ctx, job); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (w *accountWatch) dispatchCommissionFulfillment(parent context.Context, job dispatch.Job) error {
	if err := w.commissionIdentityOK(); err != nil {
		if saveErr := w.finishDispatch(job, "failed", "identity_changed", "", ""); saveErr != nil {
			return saveErr
		}
		return err
	}
	var queued dispatch.CommissionFulfillmentJob
	if json.Unmarshal(job.Data, &queued) != nil || queued.OrderID == "" || queued.OrderVersion <= 0 || queued.IntakeResult.Outcome != "ready" || queued.IntakeResult.OrderID != queued.OrderID || queued.IntakeResult.OrderVersion != queued.OrderVersion {
		return w.finishDispatch(job, "failed", "commission_fulfillment_job_invalid", "", "")
	}
	timeout := w.binding.TimeoutSeconds
	if timeout <= 0 {
		timeout = 300
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	defer cancel()
	order, err := w.readCommissionOrder(ctx, queued.OrderID)
	if err != nil {
		return w.finishDispatch(job, "failed", "commission_order_unavailable", "", "")
	}
	if order.State != "in_progress" || order.Version != queued.OrderVersion {
		return w.finishDispatch(job, "needs_user", "commission_fulfillment_order_changed", "", "")
	}
	// Cancellation interrupts local work if the identity or authoritative paid
	// order changes. No host or order mutation is attempted by this worker.
	guardDone := make(chan struct{})
	go w.guardCommissionFulfillment(ctx, cancel, order, guardDone)
	defer func() { cancel(); <-guardDone }()
	directory, err := os.MkdirTemp(w.binding.WorkDir, ".eigenflux-fulfillment-"+queued.OrderID+"-")
	if err != nil {
		return w.finishDispatch(job, "needs_user", "commission_output_directory_unavailable", "", "")
	}
	if err := w.journal.SetCommissionFulfillmentDirectory(job.ID, directory); err != nil {
		_ = os.Remove(directory) // Still empty; no Agent has received it.
		return err
	}
	prompt, required, files, cleanup, err := w.commissionAgentPrompt(ctx, job, order, map[string]any{"output_directory": directory, "intake_result": queued.IntakeResult})
	if err != nil {
		status, code := "failed", "commission_fulfillment_prompt_unavailable"
		switch {
		case errors.Is(err, dispatch.ErrNeedsUser):
			status, code = "needs_user", "commission_fulfillment_preflight_required"
		case errors.Is(err, errCommissionMaterialsMissing):
			status, code = "needs_user", "commission_materials_missing"
		case errors.Is(err, errCommissionMaterialsInvalid):
			code = "commission_materials_invalid"
		case errors.Is(err, errCommissionMaterialsUnavailable):
			code = "commission_materials_unavailable"
		}
		return w.finishDispatch(job, status, code, "", "")
	}
	defer cleanup()
	// The current immutable input set must still be the one actually inspected.
	if !validCommissionInspectedFiles(queued.IntakeResult, files, required) {
		return w.finishDispatch(job, "needs_user", "commission_fulfillment_inputs_changed", "", "")
	}
	before, err := w.readCommissionOrder(ctx, queued.OrderID)
	if err != nil || !sameCommissionIntakeOrder(order, before) || ctx.Err() != nil {
		return w.finishDispatch(job, "failed", "commission_fulfillment_cancelled_before_execution", "", "")
	}
	if err := w.commissionIdentityOK(); err != nil {
		if saveErr := w.finishDispatch(job, "failed", "identity_changed", "", ""); saveErr != nil {
			return saveErr
		}
		return err
	}
	result, runErr := w.runAgent(ctx, dispatch.Request{ID: job.ID, Kind: job.Kind, Prompt: prompt})
	if runErr != nil {
		status, code := "unknown", "commission_fulfillment_unconfirmed"
		if errors.Is(runErr, dispatch.ErrNeedsUser) || errors.Is(runErr, dispatch.ErrCommandLineTooLong) {
			status, code = "needs_user", "commission_fulfillment_permission_required"
		}
		return w.finishDispatch(job, status, code, result.SessionID, "")
	}
	decision, err := dispatch.ParseCommissionFulfillmentDecision(result.Text, job.ID, queued.OrderID, order.Version)
	if err != nil {
		return w.finishDispatch(job, "failed", "commission_fulfillment_result_invalid", result.SessionID, "")
	}
	artifacts, err := validateCommissionArtifacts(directory, decision)
	if err != nil {
		return w.finishDispatch(job, "failed", "commission_fulfillment_artifacts_invalid", result.SessionID, "")
	}
	if err := w.commissionIdentityOK(); err != nil {
		if saveErr := w.finishDispatch(job, "failed", "identity_changed", result.SessionID, ""); saveErr != nil {
			return saveErr
		}
		return err
	}
	current, err := w.readCommissionOrder(ctx, queued.OrderID)
	if err != nil || !sameCommissionIntakeOrder(order, current) || ctx.Err() != nil {
		return w.finishDispatch(job, "failed", "commission_fulfillment_order_changed", result.SessionID, "")
	}
	saved := dispatch.CommissionFulfillmentResult{Decision: decision, OutputDirectory: directory, Artifacts: artifacts}
	return auth.WithV2CredentialsLockContext(ctx, w.server.Name, 35*time.Second, func() error {
		if err := w.commissionIdentityOK(); err != nil {
			if saveErr := w.finishDispatch(job, "failed", "identity_changed", result.SessionID, ""); saveErr != nil {
				return saveErr
			}
			return err
		}
		if err := w.journal.CompleteCommissionFulfillment(job.ID, saved, result.SessionID); err != nil {
			return err
		}
		return w.emit("dispatch_status", map[string]string{"job_id": job.ID, "kind": job.Kind, "order_id": queued.OrderID, "code": "commission_fulfillment_" + decision.Outcome, "outcome": decision.Outcome})
	})
}

func (w *accountWatch) guardCommissionFulfillment(ctx context.Context, cancel context.CancelFunc, expected commissionIntakeOrder, done chan<- struct{}) {
	// Network reads must never block the one-second local identity check.
	identityDone := make(chan struct{})
	go func() {
		defer close(identityDone)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if w.commissionIdentityOK() != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-identityDone; close(done) }()
	orderTick := time.NewTicker(10 * time.Second)
	defer orderTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-orderTick.C:
			current, err := w.readCommissionOrder(ctx, strconv.FormatInt(int64(expected.OrderID), 10))
			if err != nil || !sameCommissionIntakeOrder(expected, current) {
				cancel()
				return
			}
		}
	}
}
