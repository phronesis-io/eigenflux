package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/maintenance"
)

func commissionDiagnosticHash(value string) string {
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func commissionJobObservation(job dispatch.Job) maintenance.Commission {
	c := maintenance.Commission{JobID: job.ID, JobKind: job.Kind, Role: "seller", Attempt: 1}
	var data struct {
		OrderID        string         `json:"order_id"`
		OrderVersion   int64          `json:"order_version"`
		InputRequestID string         `json:"input_request_id"`
		InputRound     int            `json:"round"`
		Notification   notificationID `json:"notification_id"`
		Payload        struct {
			OrderID notificationID `json:"order_id"`
			Version int64          `json:"order_version"`
			Role    string         `json:"recipient_role"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(job.Data, &data)
	c.OrderID, c.OrderVersion = data.OrderID, data.OrderVersion
	if data.Payload.OrderID > 0 {
		c.OrderID, c.OrderVersion, c.Role = strconv.FormatInt(int64(data.Payload.OrderID), 10), data.Payload.Version, data.Payload.Role
	}
	if data.Notification > 0 {
		c.SourceNotificationID = strconv.FormatInt(int64(data.Notification), 10)
	}
	if job.Kind == "commission_clarification" {
		c.InputRequestIDHash, c.InputRound = commissionDiagnosticHash(data.InputRequestID), data.InputRound
		c.Role = "buyer"
	}
	if c.OrderID == "" {
		retained := job.CommissionCheckpoint
		if retained == nil {
			retained = job.CommissionFulfillment
		}
		if retained != nil {
			c.OrderID, c.OrderVersion = retained.Decision.OrderID, retained.Decision.OrderVersion
		}
	}
	if job.CommissionExecution != nil {
		c.OrderID, c.OrderVersion = job.CommissionExecution.OrderID, job.CommissionExecution.OrderVersion
	}
	if job.CommissionDelivery != nil {
		c.Attempt = job.CommissionDelivery.Attempts
	}
	return c
}

func (w *accountWatch) recordCommissionObservation(c maintenance.Commission, result, code string, started time.Time, receipt dispatch.Result, cause error, fixedAttempt ...string) (recorded bool) {
	if w.binding == nil || c.OrderID == "" {
		return
	}
	if w.journal != nil && c.Stage != "diagnostic_snapshot" {
		if details, ok := w.journal.CommissionDiagnosticObservation(c.JobID); ok {
			c.LocalCode = details.LocalCode
			c.CheckReceipt = details.CheckReceipt
			if c.BridgeError == nil {
				c.BridgeError = details.BridgeError
			}
		}
	}
	if cause != nil {
		c.BridgeError = dispatch.SafeDiagnostic(cause)
	} else if result == "started" || result == "succeeded" {
		c.BridgeError = nil
	}
	attemptID := maintenance.NewID()
	if !started.IsZero() || result == "queued" {
		attemptID = commissionDiagnosticHash(fmt.Sprintf("%s:%d:%s:%d:%s:%d", c.JobID, c.OrderVersion, c.Stage, c.Attempt, c.ArtifactIDHash, started.UnixNano()))
	}
	if len(fixedAttempt) > 0 {
		attemptID = fixedAttempt[0]
	}
	e := maintenance.NewEvent(attemptID, "dispatch", "auto", "commission", result)
	e.Source, e.Platform, e.Host, e.FromVersion = "watch", runtime.GOOS, w.binding.Host, version
	if !started.IsZero() && result != "started" {
		e.DurationMS = min(max(time.Since(started).Milliseconds(), 0), int64(24*time.Hour/time.Millisecond))
	}
	if code != "" {
		e.ErrorCode = maintenance.CommissionCode(code)
	}
	if c.Adapter == "" {
		c.Adapter = w.binding.Mode
		if c.Adapter == "acp" && w.binding.Host == "workbuddy" {
			c.Adapter = "workbuddy_bridge"
		}
	}
	if len(commit) >= 8 && len(commit) <= 40 {
		if _, err := hex.DecodeString(commit); err == nil {
			c.CLICommit = commit
		}
	}
	c.HostSessionIDHash, c.HostRunIDHash = commissionDiagnosticHash(receipt.SessionID), commissionDiagnosticHash(receipt.RunID)
	var diagnostic *dispatch.DiagnosticError
	if errors.As(cause, &diagnostic) {
		if c.BridgeError != nil {
			c.Operation = c.BridgeError.Operation
		}
		e.ErrorCode = maintenance.CommissionCode(diagnostic.Code)
	}
	var apiError *client.APIError
	if errors.As(cause, &apiError) {
		c.HTTPStatus = apiError.StatusCode
	}
	e.Commission = &c
	if err := maintenance.Record(w.maintenanceScope(), e); err != nil {
		// An observation failure must never repeat or block a business operation.
		_ = w.emit("commission_telemetry_status", map[string]string{"status": "queued", "code": "local_queue_unavailable"})
		return false
	}
	return true
}

// Capture preflight failures even before a revision-fenced execution report can
// be made. A completed local job is not a completed Order.
func (w *accountWatch) observeCommissionJob(job dispatch.Job) func() {
	started := time.Now()
	c := commissionJobObservation(job)
	c.Stage = "claimed"
	if c.OrderVersion == 0 {
		c.Stage = "preflight"
	}
	w.recordCommissionObservation(c, "started", "", started, dispatch.Result{}, nil)
	return func() {
		current := job
		if w.journal != nil {
			current = w.commissionDeliveryJob(job)
		}
		current.Data = job.Data // Snapshot deliberately redacts payloads; retain only this invocation's routing data.
		c = commissionJobObservation(current)
		c.Stage = "job_finished"
		if c.OrderVersion == 0 {
			c.Stage = "preflight"
		}
		result, code := "failed", "local_state_failed"
		switch current.Status {
		case "completed":
			result, code = "succeeded", ""
		case "needs_user":
			result, code = "blocked", dispatch.CommissionExecutionCode(current.Code, current.Status)
		case "failed", "unknown":
			code = commissionJobDiagnosticCode(current.Code, current.Status)
		case "pending":
			// The retry receipt carries the reason and next-attempt time.
			return
		}
		w.recordCommissionObservation(c, result, code, started, dispatch.Result{SessionID: current.SessionID}, nil)
	}
}

func (w *accountWatch) runCommissionAgent(ctx context.Context, job dispatch.Job, request dispatch.Request) (dispatch.Result, error) {
	c := commissionJobObservation(job)
	c.Stage = "agent"
	if w.journal != nil {
		_ = w.journal.SaveCommissionDiagnostic(job.ID, &maintenance.CommissionBridgeError{Code: "unknown_failure"}, nil)
	}
	c.BridgeError = &maintenance.CommissionBridgeError{Code: "unknown_failure"}
	started := time.Now()
	w.recordCommissionObservation(c, "started", "", started, dispatch.Result{}, nil)
	result, err := w.runAgent(ctx, request)
	if err != nil && w.journal != nil {
		_ = w.journal.SaveCommissionDiagnostic(job.ID, dispatch.SafeDiagnostic(err), nil)
	}
	status, code := commissionCallOutcome(err)
	w.recordCommissionObservation(c, status, code, started, result, err)
	return result, err
}

func commissionCallOutcome(err error) (string, string) {
	switch {
	case err == nil:
		return "succeeded", ""
	case errors.Is(err, dispatch.ErrNeedsUser), errors.Is(err, dispatch.ErrCommandLineTooLong):
		return "blocked", "permission_required"
	case errors.Is(err, context.DeadlineExceeded):
		return "failed", "timeout"
	case errors.Is(err, context.Canceled):
		return "failed", "cancelled"
	case errors.Is(err, errCommissionDeliveryRejected), errors.Is(err, errCommissionDeliveryInvalid), errors.Is(err, errCommissionDeliveryGrantExpired):
		return "failed", "remote_rejected"
	case errors.Is(err, dispatch.ErrMainNoticeBusy):
		return "failed", "main_notice_busy"
	}
	var apiError *client.APIError
	if errors.As(err, &apiError) && apiError.StatusCode > 0 && apiError.StatusCode < http.StatusInternalServerError {
		return "failed", "remote_rejected"
	}
	return "failed", "agent_unconfirmed"
}

func mainNoticeObservation(item dispatch.MainNotice) maintenance.Commission {
	c := maintenance.Commission{OrderID: item.OrderID, OrderVersion: item.Version, Role: item.Role,
		JobID: commissionDiagnosticHash(item.ID), JobKind: "main_notice", Stage: "main_notice", Attempt: max(1, item.Attempts), LocalCode: maintenance.CommissionLocalCode(item.Code), CheckReceipt: &maintenance.CommissionCheckReceipt{Status: "not_applicable"}, BridgeError: item.BridgeError}
	if item.IsBuyerInputRequest() {
		c.InputRequestIDHash = commissionDiagnosticHash(item.InputRequestID)
	}
	if id, err := strconv.ParseInt(item.ID, 10, 64); err == nil && id > 0 {
		c.SourceNotificationID = strconv.FormatInt(id, 10)
	}
	return c
}

func commissionNoticeCode(code string) string {
	switch code {
	case "identity_changed":
		return "identity_changed"
	case "main_notice_target_busy":
		return "main_notice_busy"
	case "main_notice_retry_exhausted":
		return "retry_exhausted"
	case "main_notice_unconfirmed":
		return "main_notice_unconfirmed"
	case "main_notice_confirmed":
		return ""
	default:
		return "main_notice_unavailable"
	}
}

func commissionJobDiagnosticCode(code, status string) string {
	switch code {
	case "commission_order_unavailable":
		return "preflight_unavailable"
	case "commission_accept_unconfirmed":
		return "execution_unconfirmed"
	case "commission_intake_result_invalid", "commission_fulfillment_result_invalid", "commission_fulfillment_artifacts_invalid":
		return "output_invalid"
	case "identity_changed":
		return "identity_changed"
	case "commission_delivery_rejected":
		return "remote_rejected"
	}
	return dispatch.CommissionExecutionCode(code, status)
}

func (w *accountWatch) readCommissionPreflight(ctx context.Context, job dispatch.Job, id string, buyer bool) (commissionIntakeOrder, error) {
	started := time.Now()
	var order commissionIntakeOrder
	var err error
	if buyer {
		order, err = w.readBuyerClarificationOrder(ctx, id)
	} else {
		order, err = w.readCommissionOrder(ctx, id)
	}
	c := commissionJobObservation(job)
	c.Stage = "preflight"
	if err == nil {
		c.OrderVersion = order.Version
	}
	status, code := commissionCallOutcome(err)
	if err != nil && code == "agent_unconfirmed" {
		code = "preflight_unavailable"
	}
	w.recordCommissionObservation(c, status, code, started, dispatch.Result{}, err)
	return order, err
}

// Startup only queues bounded diagnostic metadata. It never claims or reruns work.
func (w *accountWatch) backfillCommissionDiagnostics() {
	if w.journal == nil || w.binding == nil {
		return
	}
	for _, snapshot := range w.journal.CommissionDiagnosticSnapshots(32) {
		c := snapshot.Observation
		if w.recordCommissionObservation(c, "observed", commissionJobDiagnosticCode(c.LocalCode, snapshot.Status), time.Time{}, dispatch.Result{}, nil, snapshot.Key) {
			if err := w.journal.AcknowledgeCommissionDiagnosticSnapshot(c.JobID, snapshot.Key); err != nil {
				_ = w.emit("commission_telemetry_status", map[string]string{"status": "queued", "code": "local_queue_unavailable"})
			}
		}
	}
}
