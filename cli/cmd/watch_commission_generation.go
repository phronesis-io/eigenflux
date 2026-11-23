package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
)

// The file list is independent of the local journal. A missing or malformed
// response cannot establish that a previous attempt left no seller output.
func (w *accountWatch) commissionGenerationHasRemoteOutput(ctx context.Context, order commissionIntakeOrder) (bool, error) {
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return false, err
	}
	id := strconv.FormatInt(int64(order.OrderID), 10)
	response, err := api.Get("/orders/"+id+"/files", nil)
	if err != nil {
		return false, err
	}
	var manifest struct {
		Outputs []json.RawMessage `json:"output_files"`
	}
	if response.Code != 0 || len(response.Data) > 256<<10 || json.Unmarshal(response.Data, &manifest) != nil || manifest.Outputs == nil {
		return false, errors.New("commission_generation_manifest_unavailable")
	}
	return len(manifest.Outputs) != 0, nil
}

func commissionGenerationExpired(order commissionIntakeOrder) bool {
	return order.DeliveryDueAt > 0 && order.DeliveryDueAt <= time.Now().UnixMilli()
}

func (w *accountWatch) deferCommissionGeneration(job dispatch.Job) error {
	if due := job.CommissionGeneration.DeliveryDueAt; due > 0 && due <= time.Now().UnixMilli() {
		return w.finishDispatch(job, "needs_user", "commission_generation_deadline_reached", job.SessionID, "")
	}
	return w.journal.ScheduleCommissionGenerationProbe(job.ID, time.Now())
}

// Check authority before inspecting the prior run. An order delivered by
// another owner ends local work without attributing their bytes to this job.
func (w *accountWatch) checkCommissionGenerationOrder(ctx context.Context, job dispatch.Job, order commissionIntakeOrder, version int64) (bool, error) {
	g := job.CommissionGeneration
	if strconv.FormatInt(int64(order.BuyerID), 10) != g.BuyerID || strconv.FormatInt(int64(order.SellerID), 10) != g.SellerID {
		return true, w.finishDispatch(job, "needs_user", "commission_fulfillment_order_changed", job.SessionID, "")
	}
	if commissionDeliveryAdvanced(order, version) {
		return true, w.finishDispatch(job, "completed", "commission_generation_delivery_observed", job.SessionID, "")
	}
	switch order.State {
	case "cancelled", "refund_pending", "refunding", "refunded":
		return true, w.finishDispatch(job, "completed", "commission_generation_order_closed", job.SessionID, "")
	}
	if order.State != "in_progress" || order.Version != version || order.DeliveryDueAt != g.DeliveryDueAt || strconv.FormatInt(int64(order.SnapshotID), 10) != g.SnapshotID {
		return true, w.finishDispatch(job, "needs_user", "commission_fulfillment_order_changed", job.SessionID, "")
	}
	if commissionGenerationExpired(order) {
		return true, w.finishDispatch(job, "needs_user", "commission_generation_deadline_reached", job.SessionID, "")
	}
	output, err := w.commissionGenerationHasRemoteOutput(ctx, order)
	if err != nil {
		return true, w.failCommissionDelivery(job, err, "commission_generation_manifest_unavailable", job.SessionID)
	}
	if output {
		return true, w.finishDispatch(job, "unknown", "commission_generation_remote_output_exists", job.SessionID, "")
	}
	return false, nil
}

// inspectPriorGeneration runs before material preparation. Only a new generation
// gets a fresh directory; a verifier pins the bytes in the original directory.
type commissionGenerationPlan struct {
	directory string
	files     []dispatch.CommissionVerifiedArtifact
	receipt   *dispatch.Result
}

func (w *accountWatch) inspectPriorGeneration(job dispatch.Job, order commissionIntakeOrder) (*commissionGenerationPlan, error) {
	g, directory := job.CommissionGeneration, job.CommissionDirectory
	id := strconv.FormatInt(int64(order.OrderID), 10)
	if filepath.Dir(directory) != filepath.Clean(w.binding.WorkDir) || !strings.HasPrefix(filepath.Base(directory), ".eigenflux-fulfillment-"+id+"-") {
		return nil, w.finishDispatch(job, "needs_user", "commission_output_directory_unavailable", job.SessionID, "")
	}
	// Prompt bytes are gated on the durable PID receipt. No PID means that
	// the new protocol never released the order to this process.
	if g.Evidence.PID != 0 {
		gone, err := dispatch.GenerationProcessGone(g.Evidence.PID)
		if err != nil {
			return nil, w.finishDispatch(job, "unknown", "commission_generation_process_unconfirmed", job.SessionID, "")
		}
		if !gone {
			return nil, w.deferCommissionGeneration(job)
		}
	}
	if g.Mode == "verify" {
		return nil, w.finishDispatch(job, "needs_user", "commission_generation_verification_interrupted", job.SessionID, "")
	}
	if receipt := g.Evidence.Result; receipt != nil {
		decision, err := dispatch.ParseCommissionFulfillmentDecision(receipt.Text, job.ID, id, order.Version)
		if err != nil {
			return nil, w.finishDispatch(job, "unknown", "commission_fulfillment_result_invalid", job.SessionID, "")
		}
		if decision.Outcome != "artifacts_ready" {
			return &commissionGenerationPlan{directory: directory, receipt: receipt}, nil
		}
	}
	files, err := snapshotCommissionGenerationArtifacts(directory)
	if err != nil || (len(files) == 0 && g.Evidence.Result != nil) {
		return nil, w.finishDispatch(job, "needs_user", "commission_fulfillment_artifacts_invalid", job.SessionID, "")
	}
	if len(files) > 0 {
		return &commissionGenerationPlan{directory: directory, files: files}, nil
	}
	if !g.LocalOnly && g.Evidence.PID != 0 {
		return nil, w.finishDispatch(job, "unknown", "commission_generation_effects_unconfirmed", job.SessionID, "")
	}
	if g.Attempts >= dispatch.CommissionGenerationMaxAttempts {
		return nil, w.finishDispatch(job, "needs_user", "commission_generation_retry_exhausted", job.SessionID, "")
	}
	next, err := os.MkdirTemp(w.binding.WorkDir, ".eigenflux-fulfillment-"+id+"-")
	if err != nil {
		return nil, w.finishDispatch(job, "needs_user", "commission_output_directory_unavailable", job.SessionID, "")
	}
	return &commissionGenerationPlan{directory: next}, nil
}

// A nil result means the job was deferred or resolved.
func (w *accountWatch) runPaidGeneration(ctx context.Context, job dispatch.Job, req dispatch.Request, order commissionIntakeOrder, plan commissionGenerationPlan) (*dispatch.Result, error) {
	g := job.CommissionGeneration
	if plan.receipt != nil {
		return plan.receipt, nil
	}
	localOnly := req.GenerationLocalDirectory != ""
	mode, attempt := "generate", 1
	if g != nil {
		attempt = g.Attempts + 1
		if len(plan.files) > 0 {
			mode, attempt = "verify", g.Attempts
			req.GenerationLocalDirectory = ""
			req = commissionGenerationVerificationPrompt(req)
		} else if localOnly != g.LocalOnly {
			return nil, w.finishDispatch(job, "needs_user", "commission_generation_tracking_unavailable", job.SessionID, "")
		}
	}
	tracked := dispatch.SupportsGenerationTracking(ctx, *w.binding)
	if g != nil && !tracked {
		if ctx.Err() != nil {
			return nil, w.deferCommissionGeneration(job)
		}
		return nil, w.finishDispatch(job, "needs_user", "commission_generation_tracking_unavailable", job.SessionID, "")
	}
	if tracked {
		if commissionGenerationExpired(order) {
			return nil, w.finishDispatch(job, "needs_user", "commission_generation_deadline_reached", job.SessionID, "")
		}
		if g != nil {
			localOnly = g.LocalOnly
		}
		identity := dispatch.CommissionGeneration{
			LocalOnly: localOnly,
			BuyerID:   strconv.FormatInt(int64(order.BuyerID), 10), SellerID: strconv.FormatInt(int64(order.SellerID), 10),
			SnapshotID: strconv.FormatInt(int64(order.SnapshotID), 10), DeliveryDueAt: order.DeliveryDueAt,
		}
		if err := w.journal.BeginCommissionGeneration(job.ID, plan.directory, mode, identity); err != nil {
			return nil, err
		}
		ctx = dispatch.WithGenerationObserver(ctx, func(e dispatch.GenerationRunEvidence) error {
			return w.journal.ObserveCommissionGeneration(job.ID, attempt, mode, e)
		})
	} else {
		req.GenerationLocalDirectory = ""
	}
	result, runErr := w.runCommissionAgent(ctx, job, req)
	if runErr == nil {
		return &result, nil
	}
	if errors.Is(runErr, dispatch.ErrNeedsUser) || errors.Is(runErr, dispatch.ErrCommandLineTooLong) {
		return nil, w.finishDispatch(job, "needs_user", "commission_fulfillment_permission_required", result.SessionID, "")
	}
	if tracked {
		return nil, w.journal.ScheduleCommissionGenerationProbe(job.ID, time.Now())
	}
	return nil, w.finishDispatch(job, "unknown", "commission_fulfillment_unconfirmed", result.SessionID, "")
}
