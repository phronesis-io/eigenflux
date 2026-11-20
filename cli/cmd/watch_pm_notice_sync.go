package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
)

func (w *accountWatch) pmOwnerNoticeSyncLoop(ctx context.Context) error {
	if !w.binding.Handles("pm_push") {
		return nil
	}
	for ctx.Err() == nil {
		if !w.handoff.begin() {
			return nil
		}
		err := w.syncPMOwnerNotice(ctx)
		w.handoff.end()
		if err != nil && ctx.Err() == nil {
			if fatal := w.diagnostic("pm_owner_notice_sync", err); fatal != nil {
				return fatal
			}
		}
		if !watchPause(ctx, 5*time.Second) {
			break
		}
	}
	return ctx.Err()
}

func (w *accountWatch) syncPMOwnerNotice(ctx context.Context) error {
	if err := w.dispatchIdentityOK(); err != nil {
		return err
	}
	item, err := dispatch.ClaimPMNoticeUpload(ctx, *w.binding, time.Now())
	if err != nil || item.ResultID == "" {
		return err
	}
	err = w.postPMOwnerNotice(ctx, item)
	if err == nil {
		return dispatch.FinishPMNoticeUpload(context.WithoutCancel(ctx), *w.binding, item)
	}
	if item.Attempts == dispatch.PMNoticeUploadLimit {
		return err
	}
	return nil
}

func (w *accountWatch) postPMOwnerNotice(ctx context.Context, item dispatch.PMNoticeUpload) error {
	client, err := w.api(ctx)
	if err != nil {
		return err
	}
	// The persisted attempt budget owns retries, including authorization failures.
	client.OnUnauthorized = nil
	client.HTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Post("/pm/owner-notices", map[string]interface{}{
		"result_id": item.ResultID, "conversation_id": item.ConversationID, "summary": item.Summary,
		"needs_action": item.NeedsAction, "generated_at": item.GeneratedAt,
	})
	if err != nil {
		return err
	}
	var receipt struct {
		ResultID string `json:"result_id"`
	}
	if response.HTTPStatus != http.StatusOK || response.Code != 0 || json.Unmarshal(response.Data, &receipt) != nil || receipt.ResultID != item.ResultID {
		return errors.New("pm_owner_notice_sync_receipt_invalid")
	}
	return nil
}
