package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/commissionapi"
	"cli.eigenflux.ai/internal/dispatch"
)

var (
	errCommissionDeliveryInvalid      = errors.New("commission_delivery_artifacts_invalid")
	errCommissionDeliveryRejected     = errors.New("commission_delivery_remote_rejected")
	errCommissionDeliveryGrantExpired = errors.New("commission_delivery_grant_expired")
)

func (w *accountWatch) commissionDeliveryJob(job dispatch.Job) dispatch.Job {
	for _, current := range w.journal.Snapshot() {
		if current.ID == job.ID {
			return current
		}
	}
	return job
}

func (w *accountWatch) scheduleCommissionDelivery(job dispatch.Job, code, session string) (bool, error) {
	scheduled, err := w.journal.ScheduleCommissionDeliveryRetry(job.ID, code, session, time.Now())
	if err != nil || !scheduled {
		return scheduled, err
	}
	current := w.commissionDeliveryJob(job)
	observation := commissionJobObservation(current)
	observation.Stage = "retry"
	observation.NextAttemptAt = current.CommissionDelivery.NextAttemptAt
	w.recordCommissionObservation(observation, "retry_scheduled", dispatch.CommissionExecutionCode(code, "failed"), time.Time{}, dispatch.Result{SessionID: session}, nil)
	return true, w.emit("dispatch_status", map[string]any{"job_id": job.ID, "kind": job.Kind, "status": current.Status, "code": current.Code, "reason": code, "delivery": current.CommissionDelivery})
}

func commissionDeliveryRetryable(err error) bool {
	if isWatchTerminal(err) || errors.Is(err, os.ErrPermission) || errors.Is(err, errCommissionDeliveryInvalid) || errors.Is(err, errCommissionDeliveryRejected) || errors.Is(err, errCommissionDeliveryGrantExpired) || errors.Is(err, errCommissionMaterialsInvalid) {
		return false
	}
	var apiError *client.APIError
	if errors.As(err, &apiError) {
		return apiError.StatusCode >= 500 || apiError.StatusCode == http.StatusRequestTimeout || apiError.StatusCode == http.StatusTooManyRequests
	}
	return true
}

func (w *accountWatch) failCommissionDelivery(job dispatch.Job, err error, code, session string) error {
	if commissionDeliveryRetryable(err) {
		if scheduled, saveErr := w.scheduleCommissionDelivery(job, code, session); scheduled || saveErr != nil {
			return saveErr
		}
		current := w.commissionDeliveryJob(job)
		if current.CommissionDelivery != nil && current.CommissionDelivery.Attempts >= dispatch.CommissionDeliveryMaxAttempts {
			observation := commissionJobObservation(current)
			observation.Stage = "retry"
			w.recordCommissionObservation(observation, "failed", "retry_exhausted", time.Now(), dispatch.Result{SessionID: session}, err)
		}
	}
	status := "failed"
	if w.commissionDeliveryJob(job).CommissionFulfillment != nil {
		status = "unknown"
	}
	if saveErr := w.finishDispatch(job, status, code, session, ""); saveErr != nil {
		return saveErr
	}
	if isWatchTerminal(err) {
		return err
	}
	return nil
}

func readCommissionDeliveryOrder(api *client.Client, id string) (commissionIntakeOrder, error) {
	var data struct {
		Order commissionIntakeOrder `json:"order"`
	}
	response, err := api.Get("/orders/"+id, nil)
	if err != nil {
		return data.Order, err
	}
	if response.Code != 0 || json.Unmarshal(response.Data, &data) != nil {
		return data.Order, errCommissionDeliveryRejected
	}
	if strconv.FormatInt(int64(data.Order.OrderID), 10) != id || data.Order.Version <= 0 || data.Order.SnapshotID <= 0 {
		return data.Order, errCommissionDeliveryRejected
	}
	return data.Order, nil
}

func commissionDeliveryParties(order commissionIntakeOrder, retry *dispatch.CommissionDeliveryRetry) bool {
	return retry != nil && strconv.FormatInt(int64(order.SellerID), 10) == retry.SellerID && strconv.FormatInt(int64(order.BuyerID), 10) == retry.BuyerID
}

func commissionDeliveryAdvanced(order commissionIntakeOrder, version int64) bool {
	if order.Version <= version {
		return false
	}
	return order.State == "validating" || order.State == "awaiting_buyer_confirmation" || order.State == "completed"
}

// Verify only the exact expected artifact bytes in the authoritative snapshot.
// The shared downloader bounds reads to the declared size plus one (64 MiB total
// was already validated), rejects redirects and does not forward API credentials.
func (w *accountWatch) verifyCommissionRemoteArtifacts(ctx context.Context, api *client.Client, order commissionIntakeOrder, result dispatch.CommissionFulfillmentResult, allowMissing bool) (map[string]bool, error) {
	dir, err := os.MkdirTemp("", "eigenflux-delivery-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	verified := make(map[string]bool)
	id := strconv.FormatInt(int64(order.OrderID), 10)
	for _, artifact := range result.Artifacts {
		if err := w.commissionFulfillmentIdentityOK(ctx); err != nil {
			return nil, err
		}
		response, err := api.Get("/orders/"+id+"/snapshots/"+strconv.FormatInt(int64(order.SnapshotID), 10)+"/download", map[string]string{"path": artifact.LogicalPath})
		if err != nil {
			var apiError *client.APIError
			if allowMissing && errors.As(err, &apiError) && apiError.StatusCode == http.StatusNotFound {
				continue
			}
			return nil, err
		}
		var data commissionapi.TransferGrantData
		if response.Code != 0 || len(response.Data) > 64<<10 || json.Unmarshal(response.Data, &data) != nil {
			return nil, errCommissionDeliveryRejected
		}
		entry := commissionMaterialEntry{LogicalPath: artifact.LogicalPath, ByteSize: &artifact.ByteSize, SHA256: artifact.SHA256}
		_, err = downloadCommissionMaterial(ctx, api.HTTPClient, data.Grant, entry, dir)
		if err != nil {
			if allowMissing && errors.Is(err, errCommissionMaterialsMissing) {
				continue
			}
			return nil, err
		}
		verified[artifact.LogicalPath] = true
	}
	return verified, nil
}

func (w *accountWatch) confirmCommissionRemoteDelivery(ctx context.Context, api *client.Client, job dispatch.Job, order commissionIntakeOrder, result dispatch.CommissionFulfillmentResult, session string) error {
	if !commissionDeliveryParties(order, job.CommissionDelivery) || !commissionDeliveryAdvanced(order, result.Decision.OrderVersion) {
		return w.failCommissionDelivery(job, errCommissionDeliveryRejected, "commission_fulfillment_order_changed", session)
	}
	started := time.Now()
	observation := commissionJobObservation(job)
	observation.Stage = "verify"
	if _, err := w.verifyCommissionRemoteArtifacts(ctx, api, order, result, false); err != nil {
		status, code := commissionCallOutcome(err)
		w.recordCommissionObservation(observation, status, code, started, dispatch.Result{SessionID: session}, err)
		return w.failCommissionDelivery(job, err, "commission_delivery_verification_unavailable", session)
	}
	w.recordCommissionObservation(observation, "succeeded", "", started, dispatch.Result{SessionID: session}, nil)
	confirmed, err := readCommissionDeliveryOrder(api, result.Decision.OrderID)
	if err != nil {
		return w.failCommissionDelivery(job, err, "commission_delivery_verification_unavailable", session)
	}
	if !sameCommissionIntakeOrder(order, confirmed) {
		// A normal platform validation transition may advance while downloading.
		if commissionDeliveryParties(confirmed, job.CommissionDelivery) && commissionDeliveryAdvanced(confirmed, result.Decision.OrderVersion) {
			return w.failCommissionDelivery(job, errors.New("commission_delivery_snapshot_advanced"), "commission_delivery_verification_unavailable", session)
		}
		return w.failCommissionDelivery(job, errCommissionDeliveryRejected, "commission_fulfillment_order_changed", session)
	}
	if err := w.commissionFulfillmentIdentityOK(ctx); err != nil {
		return w.failCommissionLocalCheck(job, err, session)
	}
	return w.finishDispatch(job, "completed", "commission_delivery_confirmed", session, "")
}

func uploadCommissionDeliveryFile(ctx context.Context, api *client.Client, base, master string, file materialFile, observe ...func(string, time.Time, error)) error {
	record := func(stage string, started time.Time, err error) {
		if len(observe) > 0 && observe[0] != nil {
			observe[0](stage, started, err)
		}
	}
	started := time.Now()
	body := map[string]any{"logical_path": file.LogicalPath, "byte_size": file.ByteSize, "sha256": file.SHA256}
	response, err := postMutation(api, base+"/uploads", "order.material", childMutationKey(master, "upload:"+file.LogicalPath), body)
	if err != nil {
		record("upload_grant", started, err)
		return err
	}
	var data commissionapi.TransferGrantData
	if response.Code != 0 || len(response.Data) > 64<<10 || json.Unmarshal(response.Data, &data) != nil || data.Grant.ObjectID <= 0 {
		record("upload_grant", started, errCommissionDeliveryRejected)
		return errCommissionDeliveryRejected
	}
	record("upload_grant", started, nil)
	started = time.Now()
	if data.Grant.URL != "" {
		if err := putCommissionDeliveryFile(ctx, api.HTTPClient, data.Grant, file); err != nil {
			record("upload_put", started, err)
			return err
		}
		record("upload_put", started, nil)
	} else if data.Grant.Method != "" {
		record("upload_put", started, errCommissionDeliveryRejected)
		return errCommissionDeliveryRejected
	}
	started = time.Now()
	response, err = postMutation(api, base+"/uploads/confirm", "order.material.confirm", childMutationKey(master, "confirm:"+file.LogicalPath), map[string]any{"object_id": data.Grant.ObjectID})
	if err != nil {
		record("upload_confirm", started, err)
		return err
	}
	if response.Code != 0 {
		record("upload_confirm", started, errCommissionDeliveryRejected)
		return errCommissionDeliveryRejected
	}
	record("upload_confirm", started, nil)
	return nil
}

func putCommissionDeliveryFile(ctx context.Context, base *http.Client, grant commissionapi.TransferGrant, file materialFile) error {
	target, err := url.Parse(grant.URL)
	if err != nil || target.Hostname() == "" || target.User != nil || target.Fragment != "" || (target.Scheme != "http" && target.Scheme != "https") || grant.Method != http.MethodPut {
		return errCommissionDeliveryRejected
	}
	if grant.ExpiresAt > 0 {
		if !time.UnixMilli(grant.ExpiresAt).After(time.Now()) {
			return errCommissionDeliveryGrantExpired
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.UnixMilli(grant.ExpiresAt))
		defer cancel()
	}
	reader, err := os.Open(file.LocalPath)
	if err != nil {
		return err
	}
	defer reader.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), reader)
	if err != nil {
		return errCommissionDeliveryRejected
	}
	request.ContentLength = file.ByteSize
	for key, value := range grant.Headers {
		switch strings.ToLower(key) {
		case "authorization", "proxy-authorization", "cookie", "host":
			return errCommissionDeliveryRejected
		case "content-length":
			if value != strconv.FormatInt(file.ByteSize, 10) {
				return errCommissionDeliveryRejected
			}
		}
		request.Header.Set(key, value)
	}
	transfer := &http.Client{Transport: base.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := transfer.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &client.APIError{StatusCode: response.StatusCode}
	}
	return nil
}
