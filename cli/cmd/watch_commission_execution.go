package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/dispatch"
)

// Reporting has its own bounded lane. An unavailable server never repeats work.
func (w *accountWatch) commissionExecutionReportLoop(ctx context.Context) error {
	if !w.receivesCommission() || w.journal == nil {
		return nil
	}
	for ctx.Err() == nil {
		if err := w.syncCommissionExecutionReports(ctx); err != nil {
			if fatal := w.diagnostic("commission_execution_report", err); fatal != nil {
				return fatal
			}
		}
		if !watchPause(ctx, 15*time.Second) {
			break
		}
	}
	return ctx.Err()
}

func (w *accountWatch) syncCommissionExecutionReports(ctx context.Context) error {
	if w.journal == nil {
		return nil
	}
	for i, report := range w.journal.CommissionExecutionReports() {
		if i >= 16 {
			break
		}
		if !w.handoff.begin() {
			return nil
		}
		err := w.sendCommissionExecutionReport(ctx, report)
		w.handoff.end()
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *accountWatch) sendCommissionExecutionReport(parent context.Context, report dispatch.CommissionExecutionReport) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return err
	}
	api.OnUnauthorized = nil
	err = auth.WithV2CredentialsLockContext(ctx, w.server.Name, 10*time.Second, func() error {
		if err := w.commissionIdentityOK(); err != nil {
			return err
		}
		credentials, err := auth.LoadV2Credentials(w.server.Name)
		if err != nil {
			return err
		}
		api.Token = credentials.AccessToken
		response, err := api.PostWithHeaders("/orders/"+report.OrderID+"/execution-report", report, map[string]string{"Idempotency-Key": "execution:" + report.Key()})
		var apiError *client.APIError
		if errors.As(err, &apiError) && apiError.StatusCode == http.StatusConflict {
			// A newer order/report owns the projection; never refresh the fence and replay.
			return w.journal.AcknowledgeCommissionExecution(report)
		}
		if err != nil {
			return err
		}
		if response.Code != 0 {
			return errors.New("commission_execution_report_rejected")
		}
		var receipt struct {
			ExecutionRevision *int64 `json:"execution_revision"`
		}
		if json.Unmarshal(response.Data, &receipt) != nil || receipt.ExecutionRevision == nil || *receipt.ExecutionRevision != report.ExpectedExecutionRevision+1 {
			return errors.New("commission_execution_report_receipt_invalid")
		}
		return w.journal.AcknowledgeCommissionExecution(report, *receipt.ExecutionRevision)
	})
	return err
}
