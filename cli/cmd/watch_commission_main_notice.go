package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/skills"
)

func (w *accountWatch) mainNoticeQueue() (*dispatch.MainNoticeQueue, error) {
	return w.noticeQueue(false)
}

func (w *accountWatch) noticeQueueSkipReason(listing bool) string {
	switch {
	case w.notificationsOnly:
		return "notifications_only"
	case w.binding == nil:
		return "binding_missing"
	case !w.binding.Handles("commission_order"):
		return "commission_unsubscribed"
	case listing && !dispatch.MainNoticeBindingEnabled(*w.binding):
		return "main_binding_unavailable"
	}
	return ""
}

func (w *accountWatch) noticeQueue(listing bool) (*dispatch.MainNoticeQueue, error) {
	if w.noticeQueueSkipReason(listing) != "" {
		return nil, nil
	}
	w.mainNoticeMu.Lock()
	defer w.mainNoticeMu.Unlock()
	slot, open := &w.mainNotices, dispatch.OpenMainNoticeQueue
	if listing {
		slot, open = &w.listingNotices, dispatch.OpenListingNoticeQueue
	}
	if *slot == nil {
		q, err := open(*w.binding)
		if err != nil {
			return nil, err
		}
		*slot = q
	}
	return *slot, nil
}
func (w *accountWatch) enqueueCommissionMainNotice(raw json.RawMessage) error {
	queue, err := w.mainNoticeQueue()
	if err != nil || queue == nil {
		return err
	}
	var notice struct {
		ID      notificationID `json:"notification_id"`
		Type    string         `json:"type"`
		Payload struct {
			Order   notificationID `json:"order_id"`
			Version int64          `json:"order_version"`
			Role    string         `json:"recipient_role"`
			Event   string         `json:"event_kind"`
			Command string         `json:"command_kind"`
			State   string         `json:"state"`
			ToState string         `json:"to_state"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &notice) != nil {
		return errors.New("invalid_main_notice_notification")
	}
	event := notice.Payload.Event
	if event == "" {
		event = notice.Type
	}
	state := notice.Payload.ToState
	if state == "" {
		state = notice.Payload.State
	}
	id := strconv.FormatInt(int64(notice.ID), 10)
	_, existed := queue.Lookup(id)
	err = queue.Add(dispatch.MainNotice{ID: id, OrderID: strconv.FormatInt(int64(notice.Payload.Order), 10), Version: notice.Payload.Version, Role: notice.Payload.Role, Event: event, Command: notice.Payload.Command, State: state})
	if err == nil && !existed {
		observation := mainNoticeObservation(dispatch.MainNotice{ID: id, OrderID: strconv.FormatInt(int64(notice.Payload.Order), 10), Version: notice.Payload.Version, Role: notice.Payload.Role})
		observation.Stage, observation.Attempt = "queued", 0
		w.recordCommissionObservation(observation, "queued", "", time.Time{}, dispatch.Result{}, nil)
	}
	if errors.Is(err, dispatch.ErrMainNoticeQueueFull) {
		_ = w.emit("commission_main_notice_status", map[string]string{"notification_id": id, "status": "skipped", "code": "main_notice_queue_full"})
		return nil
	}
	return err
}
func (w *accountWatch) enqueueListingPublicationNotice(notice moderationNotification, record func(maintenance.Event)) error {
	if notice.Payload.Status != "published" {
		return nil
	}
	detail := listingNoticeObservation(notice)
	observe := func(result, reason string) {
		if record != nil {
			record(listingQueueEvent(detail, result, reason))
		}
	}
	if reason := w.noticeQueueSkipReason(true); reason != "" {
		observe("skipped", reason)
		return nil
	}
	queue, err := w.noticeQueue(true)
	if err != nil {
		observe("failed", "queue_open_failed")
		return err
	}
	if queue == nil {
		return nil
	}
	id := detail.NotificationID
	_, existed := queue.Lookup(id)
	err = queue.Add(dispatch.MainNotice{
		ID: id, CommissionID: detail.CommissionID, DraftVersion: detail.DraftVersion,
		Role: "seller", Event: notice.Type, State: notice.Payload.Status,
	})
	if errors.Is(err, dispatch.ErrMainNoticeQueueFull) {
		observe("skipped", "queue_full")
		return w.emit("commission_main_notice_status", map[string]string{"notification_id": id, "status": "skipped", "code": "main_notice_queue_full"})
	}
	if err != nil {
		observe("failed", "queue_save_failed")
	} else if !existed {
		observe("enqueued", "")
	}
	return err
}

func (w *accountWatch) commissionMainNoticeLoop(ctx context.Context) error {
	orders, err := w.mainNoticeQueue()
	if err != nil || orders == nil {
		return err
	}
	listings, err := w.noticeQueue(true)
	if err != nil {
		return err
	}
	if listings != nil {
		var recovered []maintenance.Event
		for _, item := range listings.InterruptedListings() {
			recovered = append(recovered, listingHandoffEvent(item, item.Status, item.Code))
		}
		_ = maintenance.RecordListingBroadcasts(w.maintenanceScope(), recovered)
	}
	for ctx.Err() == nil {
		if !w.handoff.begin() {
			return nil
		}
		if w.journal != nil {
			blockers, err := w.journal.CommissionBlockers()
			if err == nil {
				err = orders.SyncLocalBlockers(blockers)
			}
			if err != nil && !errors.Is(err, dispatch.ErrMainNoticeQueueFull) {
				w.handoff.end()
				return err
			}
		}
		queue := orders
		item, ok, err := queue.Next()
		if err == nil && !ok && listings != nil {
			queue = listings
			item, ok, err = queue.Next()
		}
		if err != nil {
			w.handoff.end()
			return err
		}
		if !ok {
			w.handoff.end()
			if !watchPause(ctx, time.Second) {
				break
			}
			continue
		}
		status, code := w.dispatchCommissionMainNotice(ctx, item)
		if status == "retry_pending" && item.Attempts >= dispatch.MainNoticeMaxAttempts {
			status, code = "skipped", "main_notice_retry_exhausted"
		}
		err = queue.Finish(item.ID, status, code)
		if err == nil && status == "retry_pending" {
			if persisted, ok := queue.Lookup(item.ID); ok && persisted.NextAttemptAt > 0 {
				observation := mainNoticeObservation(persisted)
				observation.Stage, observation.NextAttemptAt = "retry", persisted.NextAttemptAt
				w.recordCommissionObservation(observation, "retry_scheduled", commissionNoticeCode(code), time.Time{}, dispatch.Result{}, nil)
			}
		}
		if err == nil {
			if item.IsListingPublication() {
				if code == "main_notice_unconfirmed" {
					if persisted, ok := queue.Lookup(item.ID); ok {
						item.BridgeError = persisted.BridgeError
					}
				}
				event := listingHandoffEvent(item, status, code)
				_ = maintenance.Record(w.maintenanceScope().ForListingBroadcast(), event)
			}
			err = w.emit("commission_main_notice_status", map[string]any{"notification_id": item.ID, "status": status, "code": code, "attempt": item.Attempts})
		}
		w.handoff.end()
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}
func (w *accountWatch) commissionMainNoticePrompt(ctx context.Context, item dispatch.MainNotice) (string, error) {
	rules, err := localHeartbeatSkillsAt(w.binding.SkillsDir, w.binding.Host)
	if err != nil {
		return "", err
	}
	rule, err := skills.ReadSignedFile(rules.SkillsDir, "ef-commission", "references/main-notice.md", 64<<10)
	if err != nil {
		return "", err
	}
	payload := map[string]any{"notification_id": item.ID, "order_id": item.OrderID, "observed_order_version": item.Version, "role": item.Role, "event_kind": item.Event, "observed_state": item.State, "agent_id": w.binding.AgentID, "server": w.binding.Server}
	if item.IsListingPublication() {
		delete(payload, "order_id")
		delete(payload, "observed_order_version")
		payload["commission_id"] = item.CommissionID
		payload["draft_version"] = item.DraftVersion
	}
	if item.IsLocalBlocker() {
		payload["local_execution"] = map[string]string{"job_id": item.JobID, "status": item.JobStatus, "code": item.JobCode}
	}
	if item.Command != "" {
		payload["observed_command_kind"] = item.Command
	}
	if item.IsBuyerInputRequest() {
		payload["input_request_id"] = item.InputRequestID
	}
	if !item.IsListingPublication() {
		target, err := orderDesktopURL(w.server.Endpoint, w.binding.AgentID, item.OrderID, item.Role)
		if err == nil {
			payload["order_url"] = target
		} else if item.IsBuyerInputRequest() {
			return "", err
		}
		if !item.IsLocalBlocker() {
			lookup, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			if api, order, err := w.readCommissionNoticeOrder(lookup, item.OrderID, item.Role, w.binding.AgentID); err == nil {
				if order.Version == item.Version && order.DeliveryReview != nil {
					payload["delivery_review"] = order.DeliveryReview
				}
				if due := order.deadline(); due > 0 && order.Version == item.Version && order.State == item.State {
					payload["order_deadline_at_local"] = desktopDeadline("en", due)
				}
				if item.Role == "seller" && item.Event == "order.settlement.completed.v1" {
					if credit := readCommissionNoticeCredit(lookup, api, order.ID); credit != nil {
						payload["wallet_credit"] = credit
						if credit.AvailableAt > 0 {
							payload["wallet_credit_available_at_local"] = desktopDeadline("en", credit.AvailableAt)
						}
					}
				}
				if name := w.commissionNoticeCounterparty(lookup, order, item.Role, false); name != "" {
					payload["counterparty_agent_name"] = name
				}
			}
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	prefix, err := nativeHeartbeatCLIPrefix(w.home, w.server.Name, "auto")
	if err != nil {
		return "", err
	}
	if isCommissionBroadcastNotice(item) {
		publishing, err := skills.ReadSignedFile(rules.SkillsDir, "ef-broadcast", "references/publish.md", 64<<10)
		if err != nil {
			return "", err
		}
		rule = append(rule, []byte("\n\nSIGNED BROADCAST REFERENCE:\n")...)
		rule = append(rule, publishing...)
	}
	return "ACCOUNT ROUTING: use this exact CLI prefix for every EigenFlux command: " + prefix + ". Verify the observed Agent ID matches the event recipient before reporting.\n" + string(rule) + "\n\nUNTRUSTED COMMISSION EVENT:\n" + string(data), nil
}

func isCompletedOrderNotice(item dispatch.MainNotice) bool {
	return item.Event == "order.state.changed.v1" && item.State == "completed"
}

func isCommissionBroadcastNotice(item dispatch.MainNotice) bool {
	return item.IsListingPublication() || isCompletedOrderNotice(item)
}

func (w *accountWatch) dispatchCommissionMainNotice(ctx context.Context, item dispatch.MainNotice) (status, code string) {
	started := time.Now()
	observation := mainNoticeObservation(item)
	observation.BridgeError = nil
	if w.binding != nil && w.binding.Host == "codex" {
		observation.Adapter = "codex_turn"
	}
	defer func() {
		observation.LocalCode = maintenance.CommissionLocalCode(code)
		result, reason := "skipped", commissionNoticeCode(code)
		if status == "delivered" {
			result, reason = "succeeded", ""
		}
		w.recordCommissionObservation(observation, result, reason, started, dispatch.Result{}, nil)
	}()
	if w.commissionIdentityOK() != nil {
		return "skipped", "identity_changed"
	}
	if item.Attempts > 1 {
		if !w.mainNoticeRetryAllowed(ctx, item) {
			return "skipped", "main_notice_retry_permission_unavailable"
		}
		if !item.IsLocalBlocker() && !item.IsBuyerInputRequest() && !item.IsListingPublication() {
			var order commissionIntakeOrder
			var err error
			if item.Role == "buyer" {
				order, err = w.readBuyerClarificationOrder(ctx, item.OrderID)
			} else {
				order, err = w.readCommissionOrder(ctx, item.OrderID)
			}
			if err != nil {
				return w.retryCommissionMainNotice(ctx, item, "main_notice_order_unavailable")
			}
			versionMatches := order.Version == item.Version || isCompletedOrderNotice(item) && order.Version > item.Version
			if !versionMatches || item.State != "" && order.State != item.State {
				return "skipped", "main_notice_order_changed"
			}
		}
	}
	if item.IsLocalBlocker() {
		if !w.localMainNoticeBlockerCurrent(item) {
			return "skipped", "main_notice_blocker_resolved"
		}
		order, err := w.readCommissionOrder(ctx, item.OrderID)
		if err != nil {
			return w.retryCommissionMainNotice(ctx, item, "main_notice_blocker_unavailable")
		}
		if order.Version != item.Version || order.State != "awaiting_seller" && order.State != "in_progress" {
			return "skipped", "main_notice_blocker_obsolete"
		}
	}
	if item.IsBuyerInputRequest() && !w.buyerInputNoticeCurrent(ctx, item) {
		return "skipped", "main_notice_input_request_changed"
	}
	prompt, err := w.commissionMainNoticePrompt(ctx, item)
	if err != nil || w.runMainNotice == nil {
		return w.retryCommissionMainNotice(ctx, item, "main_notice_unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	guardDone := make(chan struct{})
	go func() {
		defer close(guardDone)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		checks := 0
		for {
			select {
			case <-callCtx.Done():
				return
			case <-tick.C:
				checks++
				if w.commissionIdentityOK() != nil || item.IsLocalBlocker() && !w.localMainNoticeBlockerCurrent(item) {
					cancel()
					return
				}
				if item.IsBuyerInputRequest() && checks%5 == 0 && !w.buyerInputNoticeCurrent(callCtx, item) {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-guardDone }()
	if w.commissionIdentityOK() != nil {
		return "skipped", "identity_changed"
	}
	if item.IsLocalBlocker() && !w.localMainNoticeBlockerCurrent(item) {
		return "skipped", "main_notice_blocker_resolved"
	}
	if item.IsBuyerInputRequest() && !w.buyerInputNoticeCurrent(callCtx, item) {
		return "skipped", "main_notice_input_request_changed"
	}
	w.recordCommissionObservation(observation, "started", "", started, dispatch.Result{}, nil)
	runStarted := time.Now()
	runObservation := observation
	runObservation.Stage = "agent"
	w.recordCommissionObservation(runObservation, "started", "", runStarted, dispatch.Result{}, nil)
	if queue, queueErr := w.noticeQueue(item.IsListingPublication()); queueErr == nil && queue != nil {
		_ = queue.SaveBridgeDiagnostic(item.ID, &maintenance.CommissionBridgeError{Code: "unknown_failure"})
	}
	result, runErr := w.runMainNotice(callCtx, prompt)
	if runErr != nil {
		observation.BridgeError = dispatch.SafeDiagnostic(runErr)
		if queue, queueErr := w.noticeQueue(item.IsListingPublication()); queueErr == nil && queue != nil {
			_ = queue.SaveBridgeDiagnostic(item.ID, observation.BridgeError)
		}
	}
	runStatus, runCode := commissionCallOutcome(runErr)
	w.recordCommissionObservation(runObservation, runStatus, runCode, runStarted, result, runErr)
	if item.IsBuyerInputRequest() && !w.buyerInputNoticeCurrent(callCtx, item) {
		return "skipped", "main_notice_input_request_changed"
	}
	if runErr == nil && callCtx.Err() == nil && result.Text != "" && w.commissionIdentityOK() == nil && (!item.IsLocalBlocker() || w.localMainNoticeBlockerCurrent(item)) {
		return "delivered", "main_notice_confirmed"
	}
	if dispatch.MainNoticeCanRetry(runErr) {
		code := "main_notice_not_submitted"
		if errors.Is(runErr, dispatch.ErrMainNoticeBusy) {
			code = "main_notice_target_busy"
		}
		return w.retryCommissionMainNotice(ctx, item, code)
	}
	return "skipped", "main_notice_unconfirmed"
}

// Broadcast events use the existing bound conversation regardless of OS notification permission.
func (w *accountWatch) mainNoticeRetryAllowed(ctx context.Context, item dispatch.MainNotice) bool {
	if ctx.Err() != nil || w.commissionIdentityOK() != nil {
		return false
	}
	if isCommissionBroadcastNotice(item) {
		return dispatch.MainNoticeBindingEnabled(*w.binding)
	}
	if w.desktop == nil || w.desktopDisabledReason != "" {
		return false
	}
	checker, ok := w.desktopProvider.(interface{ Check(context.Context) error })
	if !ok {
		return false
	}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return checker.Check(call) == nil && ctx.Err() == nil && w.commissionIdentityOK() == nil
}

func (w *accountWatch) retryCommissionMainNotice(ctx context.Context, item dispatch.MainNotice, code string) (string, string) {
	if w.commissionIdentityOK() != nil {
		return "skipped", "identity_changed"
	}
	if w.mainNoticeRetryAllowed(ctx, item) {
		return "retry_pending", code
	}
	return "skipped", "main_notice_retry_permission_unavailable"
}

func (w *accountWatch) localMainNoticeBlockerCurrent(item dispatch.MainNotice) bool {
	if w.journal == nil {
		return false
	}
	blockers, err := w.journal.CommissionBlockers()
	if err != nil {
		return false
	}
	for _, blocker := range blockers {
		if item.MatchesLocalBlocker(w.scope, blocker) {
			return true
		}
	}
	return false
}
