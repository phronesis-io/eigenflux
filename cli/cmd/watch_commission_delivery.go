package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strconv"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
)

// Persist exact evidence before every remote mutation. A scheduled retry keeps
// its original directory, artifact manifest and idempotency keys.
func (w *accountWatch) deliverCommissionArtifacts(ctx context.Context, job dispatch.Job, order commissionIntakeOrder, result dispatch.CommissionFulfillmentResult, session string) error {
	files, cleanup, err := freezeCommissionArtifacts(result)
	if err != nil {
		return w.finishDispatch(job, "failed", "commission_fulfillment_artifacts_invalid", session, "")
	}
	defer cleanup()
	job = w.commissionDeliveryJob(job)
	if job.CommissionDelivery == nil {
		if job.CommissionCheckpoint == nil {
			if err := w.journal.SaveCommissionCheckpoint(job.ID, result, session); err != nil {
				return err
			}
		}
		if err := w.journal.InitializeCommissionDelivery(job.ID, strconv.FormatInt(int64(order.BuyerID), 10), strconv.FormatInt(int64(order.SellerID), 10), strconv.FormatInt(int64(order.SnapshotID), 10)); err != nil {
			return err
		}
		job = w.commissionDeliveryJob(job)
	}
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return w.failCommissionLocalCheck(job, err, session)
	}
	id := result.Decision.OrderID
	entered := false
	err = auth.WithV2CredentialsLockContext(ctx, w.server.Name, 35*time.Second, func() error {
		entered = true
		if err := w.commissionFulfillmentIdentityOK(ctx); err != nil {
			return w.failCommissionLocalCheck(job, err, session)
		}
		credentials, err := auth.LoadV2Credentials(w.server.Name)
		if err != nil {
			return w.failCommissionLocalCheck(job, err, session)
		}
		api.Token, api.OnUnauthorized = credentials.AccessToken, nil
		before, err := readCommissionDeliveryOrder(api, id)
		if err != nil {
			return w.failCommissionDelivery(job, err, "commission_order_unavailable", session)
		}
		if !commissionDeliveryParties(before, job.CommissionDelivery) {
			return w.failCommissionDelivery(job, errCommissionDeliveryRejected, "commission_fulfillment_order_changed", session)
		}
		if commissionDeliveryAdvanced(before, result.Decision.OrderVersion) {
			return w.confirmCommissionRemoteDelivery(ctx, api, job, before, result, session)
		}
		if !sameCommissionIntakeOrder(order, before) || before.State != "in_progress" || before.Version != result.Decision.OrderVersion || strconv.FormatInt(int64(before.SnapshotID), 10) != job.CommissionDelivery.SnapshotID {
			return w.failCommissionDelivery(job, errCommissionDeliveryRejected, "commission_fulfillment_order_changed", session)
		}
		uploaded := map[string]bool{}
		if job.CommissionDelivery.Attempts > 1 || job.CommissionFulfillment != nil {
			uploaded, err = w.verifyCommissionRemoteArtifacts(ctx, api, before, result, true)
			if err != nil {
				return w.failCommissionDelivery(job, err, "commission_delivery_verification_unavailable", session)
			}
			current, err := readCommissionDeliveryOrder(api, id)
			if err != nil {
				return w.failCommissionDelivery(job, err, "commission_order_unavailable", session)
			}
			if !sameCommissionIntakeOrder(before, current) {
				if commissionDeliveryParties(current, job.CommissionDelivery) && commissionDeliveryAdvanced(current, result.Decision.OrderVersion) {
					return w.confirmCommissionRemoteDelivery(ctx, api, job, current, result, session)
				}
				return w.failCommissionDelivery(job, errCommissionDeliveryRejected, "commission_fulfillment_order_changed", session)
			}
		}
		if err := w.commissionFulfillmentIdentityOK(ctx); err != nil {
			return w.failCommissionLocalCheck(job, err, session)
		}
		if err := w.journal.BeginCommissionDelivery(job.ID, result, session); err != nil {
			return err
		}
		job = w.commissionDeliveryJob(job)
		base := "/orders/" + id
		key := childMutationKey("watch-delivery-"+job.ID, result.OutputDirectory)
		for _, file := range files {
			if err := w.commissionFulfillmentIdentityOK(ctx); err != nil {
				return w.failCommissionLocalCheck(job, err, session)
			}
			if uploaded[file.LogicalPath] {
				continue
			}
			if err := uploadCommissionDeliveryFile(ctx, api, base, key, file); err != nil {
				return w.failCommissionDelivery(job, err, "commission_delivery_upload_unconfirmed", session)
			}
		}
		current, err := readCommissionDeliveryOrder(api, id)
		if err != nil {
			return w.failCommissionDelivery(job, err, "commission_order_unavailable", session)
		}
		if commissionDeliveryParties(current, job.CommissionDelivery) && commissionDeliveryAdvanced(current, result.Decision.OrderVersion) {
			return w.confirmCommissionRemoteDelivery(ctx, api, job, current, result, session)
		}
		if !sameCommissionIntakeOrder(order, current) {
			return w.failCommissionDelivery(job, errCommissionDeliveryRejected, "commission_fulfillment_order_changed", session)
		}
		if err := w.commissionFulfillmentIdentityOK(ctx); err != nil {
			return w.failCommissionLocalCheck(job, err, session)
		}
		response, err := api.PostWithHeaders(base+"/deliver", map[string]any{"expected_version": result.Decision.OrderVersion, "output_files": files}, map[string]string{idempotencyHeader: childMutationKey(key, "deliver")})
		if err != nil {
			return w.failCommissionDelivery(job, err, "commission_delivery_unconfirmed", session)
		}
		if response.Code != 0 {
			return w.failCommissionDelivery(job, errCommissionDeliveryRejected, "commission_delivery_rejected", session)
		}
		confirmed, err := readCommissionDeliveryOrder(api, id)
		if err != nil {
			return w.failCommissionDelivery(job, err, "commission_delivery_unconfirmed", session)
		}
		return w.confirmCommissionRemoteDelivery(ctx, api, job, confirmed, result, session)
	})
	if err != nil && !entered {
		return w.failCommissionLocalCheck(job, err, session)
	}
	return err
}

// Copy only the verified bounded bytes through a confined root. Uploads never
// reopen an Agent-controlled path after the digest check.
func freezeCommissionArtifacts(result dispatch.CommissionFulfillmentResult) ([]materialFile, func(), error) {
	noop := func() {}
	info, err := os.Lstat(result.OutputDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, noop, errors.New("commission_artifact_directory_invalid")
	}
	root, err := os.OpenRoot(result.OutputDirectory)
	if err != nil {
		return nil, noop, err
	}
	defer root.Close()
	dir, err := os.MkdirTemp("", "eigenflux-delivery-")
	if err != nil {
		return nil, noop, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	files := make([]materialFile, 0, len(result.Artifacts))
	for index, artifact := range result.Artifacts {
		if _, err := commissionArtifactPathInfo(root, artifact.RelativePath); err != nil {
			cleanup()
			return nil, noop, err
		}
		file, err := root.Open(artifact.RelativePath)
		if err != nil {
			cleanup()
			return nil, noop, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, artifact.ByteSize+1))
		_ = file.Close()
		digest := sha256.Sum256(data)
		if readErr != nil || int64(len(data)) != artifact.ByteSize || hex.EncodeToString(digest[:]) != artifact.SHA256 {
			cleanup()
			return nil, noop, errors.New("commission_artifact_changed")
		}
		local := dir + "/" + strconv.Itoa(index)
		if err := os.WriteFile(local, data, 0600); err != nil {
			cleanup()
			return nil, noop, err
		}
		files = append(files, materialFile{LogicalPath: artifact.LogicalPath, SHA256: artifact.SHA256, ByteSize: artifact.ByteSize, LocalPath: local})
	}
	return files, cleanup, nil
}
