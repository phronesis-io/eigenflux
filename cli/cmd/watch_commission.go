package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
)

const commissionPollPages = 32

// Input coordination is Agent-only work. It must not become a desktop or
// main-conversation notice merely because its Order remains awaiting_seller.
func isCommissionInputCoordination(raw json.RawMessage) bool {
	var notice struct {
		Type    string `json:"type"`
		Payload struct {
			Event string `json:"event_kind"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &notice) != nil {
		return false
	}
	event := notice.Payload.Event
	if event == "" {
		event = notice.Type
	}
	return event == "order.inputs.requested.v1" || event == "order.inputs.supplied.v1" || event == "order.inputs.withdrawn.v1"
}

type commissionNotificationPage struct {
	Notifications []json.RawMessage `json:"notifications"`
	HasMore       bool              `json:"has_more"`
	NextCursor    string            `json:"next_cursor"`
}

// This result is produced only after every successful row has been ACKed and
// required output has succeeded. Fatal errors never carry this paging signal.
type moderationPageError struct{ cause error }

func (e *moderationPageError) Error() string { return e.cause.Error() }
func (e *moderationPageError) Unwrap() error { return e.cause }

func decodeCommissionNotificationPage(data json.RawMessage) (commissionNotificationPage, error) {
	var page commissionNotificationPage
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil ||
		len(fields["notifications"]) == 0 || bytes.Equal(bytes.TrimSpace(fields["notifications"]), []byte("null")) ||
		json.Unmarshal(data, &page) != nil || len(page.Notifications) > 100 {
		return page, errors.New("invalid_commission_notification_page")
	}
	return page, nil
}

func (w *accountWatch) receivesCommission() bool {
	return w.notificationsOnly || (w.binding != nil && w.journal != nil && w.binding.Handles("commission_order"))
}

// Moderation is informational and never requires an Order execution subscription.
func (w *accountWatch) receivesCommissionNotifications() bool {
	return w.identity.AgentID != "" && w.scope != ""
}

func (w *accountWatch) deliverSocketEvent(ctx context.Context, kind string, data json.RawMessage) error {
	if kind == "notification_push" && w.receivesCommissionNotifications() {
		var page struct {
			Notifications []json.RawMessage `json:"notifications"`
		}
		if json.Unmarshal(data, &page) == nil {
			for _, raw := range page.Notifications {
				var header struct {
					Source string `json:"source_type"`
				}
				if json.Unmarshal(raw, &header) == nil && (header.Source == moderationNotificationSource || header.Source == "commission_order" && w.receivesCommission()) {
					err := w.deliverCommissionNotifications(ctx, data, "socket")
					var partial *moderationPageError
					if err != nil && errors.As(err, &partial) {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						return w.emit("diagnostic", map[string]string{"area": "moderation_delivery", "code": "moderation_sink_failed"})
					}
					return err
				}
			}
		}
		// Legacy notifications need no HTTP client, token refresh, or Commission owner.
	}
	return w.deliverPM(ctx, kind, data, "socket")
}

// Notification-only bindings use HTTP: the current server WebSocket also marks
// PMs read. A binding owning both sources can reuse its existing PM connection.
func (w *accountWatch) commissionFallbackLoop(ctx context.Context) error {
	if !w.receivesCommissionNotifications() || !w.receivesCommission() && w.receivesPM() {
		return nil
	}
	interval := time.Minute
	if !w.receivesCommission() {
		interval = moderationReconcileInterval
	}
	for ctx.Err() == nil {
		if err := w.pollCommissionNotifications(ctx); err != nil {
			if w.notificationsOnly && w.notificationsReady.Swap(false) {
				_ = w.emit("notifications_unavailable", w.runtimeData())
			}
			if fatal := w.diagnostic("commission_poll", err); fatal != nil {
				return fatal
			}
		} else if w.notificationsOnly && w.notificationsReady.CompareAndSwap(false, true) {
			if err := w.emit("notifications_ready", w.runtimeData()); err != nil {
				return err
			}
		}
		if !watchPause(ctx, interval) {
			break
		}
	}
	return ctx.Err()
}

func (w *accountWatch) pollCommissionNotifications(ctx context.Context) error {
	if !w.receivesCommissionNotifications() {
		return nil
	}
	var deliveryErrors []error
	cursor := ""
	seen := map[string]bool{"": true}
	for pageIndex := 0; pageIndex < commissionPollPages; pageIndex++ {
		if err := w.dispatchIdentityOK(); err != nil {
			return errors.Join(errors.Join(deliveryErrors...), err)
		}
		api, err := w.api(ctx)
		if err != nil {
			return errors.Join(errors.Join(deliveryErrors...), err)
		}
		params := map[string]string{"limit": "50"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		response, err := api.Get("/notifications/pending", params)
		if err != nil {
			return errors.Join(errors.Join(deliveryErrors...), err)
		}
		if response.Code != 0 {
			return errors.Join(errors.Join(deliveryErrors...), errors.New("commission_poll_failed"))
		}
		page, err := decodeCommissionNotificationPage(response.Data)
		if err != nil {
			return errors.Join(errors.Join(deliveryErrors...), err)
		}
		if err := w.deliverCommissionNotifications(ctx, response.Data, "http_poll"); err != nil {
			var partial *moderationPageError
			if ctx.Err() != nil || isWatchTerminal(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &partial) {
				return errors.Join(errors.Join(deliveryErrors...), err)
			}
			deliveryErrors = append(deliveryErrors, partial.cause)
		}
		if !page.HasMore {
			return errors.Join(deliveryErrors...)
		}
		if seen[page.NextCursor] {
			return errors.Join(errors.Join(deliveryErrors...), errors.New("commission_notification_cursor_did_not_advance"))
		}
		cursor = page.NextCursor
		seen[cursor] = true
	}
	return errors.Join(deliveryErrors...)
}

func (w *accountWatch) deliverCommissionNotifications(ctx context.Context, data json.RawMessage, transport string) error {
	if !w.receivesCommissionNotifications() {
		return nil
	}
	page, err := decodeCommissionNotificationPage(data)
	if err != nil {
		return err
	}
	api, err := w.api(ctx)
	if err != nil {
		return err
	}
	return auth.WithV2CredentialsLockContext(ctx, w.server.Name, 35*time.Second, func() error {
		if err := w.dispatchIdentityOK(); err != nil {
			return err
		}
		knownJobs := map[string]bool{}
		if w.journal != nil {
			for _, job := range w.journal.Snapshot() {
				knownJobs[job.ID] = true
			}
		}
		ack := make([]map[string]string, 0, len(page.Notifications))
		genericOutput := make([]json.RawMessage, 0, len(page.Notifications))
		var sinkErrors []error
		for _, raw := range page.Notifications {
			if err := ctx.Err(); err != nil {
				return err
			}
			var notification struct {
				Source string          `json:"source_type"`
				ID     json.RawMessage `json:"notification_id"`
			}
			if json.Unmarshal(raw, &notification) != nil {
				genericOutput = append(genericOutput, raw)
				continue
			}
			if notification.Source == moderationNotificationSource {
				notice, err := parseModerationNotification(raw, w.identity.AgentID)
				if err != nil {
					if err := w.emit("commission_notification_invalid", map[string]string{"source_type": moderationNotificationSource, "code": "invalid_moderation_notification"}); err != nil {
						return err
					}
					continue
				}
				saved, err := w.enqueueModerationNotification(notice)
				if err != nil {
					var sink *moderationSinkError
					if ctx.Err() != nil || isWatchTerminal(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &sink) {
						return err
					}
					sinkErrors = append(sinkErrors, err)
					continue
				}
				if saved {
					var id notificationID
					if err := json.Unmarshal(notification.ID, &id); err != nil {
						return err
					}
					ack = append(ack, map[string]string{"notification_id": strconv.FormatInt(int64(id), 10), "source_type": moderationNotificationSource})
				}
				continue
			}
			genericOutput = append(genericOutput, raw)
			if notification.Source != "commission_order" || !w.receivesCommission() {
				continue
			}
			internal := isCommissionInputCoordination(raw)
			// A notifications-only consumer cannot handle Agent coordination.
			// Keep it pending on the server until an execution owner can persist it.
			if internal && w.journal == nil {
				continue
			}
			if w.journal != nil {
				if err := w.journal.AddCommissionNotification(raw); err != nil {
					return errors.Join(errWatchDispatchJournal, err)
				}
				for _, job := range w.journal.Snapshot() {
					if job.Kind != "commission_order" || knownJobs[job.ID] {
						continue
					}
					knownJobs[job.ID] = true
					job.Data = raw
					observation := commissionJobObservation(job)
					observation.Stage, observation.Attempt, observation.Transport = "queued", 0, transport
					w.recordCommissionObservation(observation, "queued", "", time.Time{}, dispatch.Result{}, nil)
				}
			}
			if w.notificationsOnly && w.desktop == nil {
				return errors.New("notification_desktop_queue_unavailable")
			}
			if !internal {
				if err := w.enqueueCommissionMainNotice(raw); err != nil {
					return errors.Join(errWatchDispatchJournal, err)
				}
				if err := w.enqueueDesktopCommission(raw); err != nil {
					return errors.Join(errWatchDispatchJournal, err)
				}
			}
			var id notificationID
			if json.Unmarshal(notification.ID, &id) != nil {
				return errors.New("invalid_commission_notification_id")
			}
			receiptKind := "desktop_saved"
			if w.journal != nil {
				receiptKind = "execution_enqueued"
			}
			ack = append(ack, map[string]string{
				"notification_id": strconv.FormatInt(int64(id), 10),
				"source_type":     "commission_order",
				"receipt_kind":    receiptKind,
			})
		}
		// Every notification is durable before any ACK. Refresh outside the
		// credential lock; an uncertain ACK is safe to repeat after deduplication.
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.dispatchIdentityOK(); err != nil {
			return err
		}
		api.OnUnauthorized = nil
		credentials, err := auth.LoadV2Credentials(w.server.Name)
		if err != nil {
			return err
		}
		api.Token = credentials.AccessToken
		ackedOrders := 0
		for first := 0; first < len(ack); first += 50 {
			last := min(first+50, len(ack))
			response, err := postNotificationACK(api, ack[first:last])
			if err != nil {
				return err
			}
			if response.Code != 0 {
				return errors.New("commission_notification_ack_failed")
			}
			for _, item := range ack[first:last] {
				if item["source_type"] == "commission_order" {
					ackedOrders++
				}
			}
		}
		// Ordinary reconciliation owns moderation only; realtime socket forwarding
		// still preserves all legacy non-moderation entries.
		var rowFailure error
		if len(sinkErrors) > 0 {
			rowFailure = &moderationPageError{cause: errors.Join(sinkErrors...)}
		}
		if len(genericOutput) == 0 || transport == "http_poll" && !w.receivesCommission() {
			return rowFailure
		}
		if w.notificationsOnly {
			if ackedOrders == 0 {
				return rowFailure
			}
			if err := w.emit("notification_push", map[string]interface{}{"count": ackedOrders, "transport": transport}); err != nil {
				return err
			}
			return rowFailure
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(data, &envelope); err != nil {
			return err
		}
		output := make(map[string]interface{}, len(envelope)+2)
		for key, value := range envelope {
			output[key] = value
		}
		output["notifications"] = genericOutput
		output["transport"], output["received_at"] = transport, time.Now().UTC().Format(time.RFC3339Nano)
		if err := w.emit("notification_push", output); err != nil {
			return err
		}
		return rowFailure
	})
}

const moderationReconcileInterval = 15 * time.Minute

// Socket connection/reconnection already replays pending notifications. HTTP
// covers an unavailable socket and infrequent lost Pub/Sub wake-ups.
func (w *accountWatch) reconcileModeration(ctx context.Context, last, now time.Time) (time.Time, error) {
	if w.receivesCommission() || !w.receivesPM() || w.wsOnline.Load() && now.Sub(last) < moderationReconcileInterval {
		return last, nil
	}
	return now, w.pollCommissionNotifications(ctx)
}
