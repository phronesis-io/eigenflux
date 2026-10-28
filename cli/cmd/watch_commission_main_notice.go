package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/skills"
)

func (w *accountWatch) mainNoticeQueue() (*dispatch.MainNoticeQueue, error) {
	if w.notificationsOnly || w.binding == nil || !w.binding.Handles("commission_order") {
		return nil, nil
	}
	w.mainNoticeMu.Lock()
	defer w.mainNoticeMu.Unlock()
	if w.mainNotices == nil {
		q, err := dispatch.OpenMainNoticeQueue(*w.binding)
		if err != nil {
			return nil, err
		}
		w.mainNotices = q
	}
	return w.mainNotices, nil
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
	err = queue.Add(dispatch.MainNotice{ID: id, OrderID: strconv.FormatInt(int64(notice.Payload.Order), 10), Version: notice.Payload.Version, Role: notice.Payload.Role, Event: event, State: state})
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
func (w *accountWatch) commissionMainNoticeLoop(ctx context.Context) error {
	queue, err := w.mainNoticeQueue()
	if err != nil || queue == nil {
		return err
	}
	for ctx.Err() == nil {
		if !w.handoff.begin() {
			return nil
		}
		if w.journal != nil {
			blockers, err := w.journal.CommissionBlockers()
			if err == nil {
				err = queue.SyncLocalBlockers(blockers)
			}
			if err != nil && !errors.Is(err, dispatch.ErrMainNoticeQueueFull) {
				w.handoff.end()
				return err
			}
		}
		item, ok, err := queue.Next()
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
			err = w.emit("commission_main_notice_status", map[string]any{"notification_id": item.ID, "status": status, "code": code, "attempt": item.Attempts})
		}
		w.handoff.end()
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}
func (w *accountWatch) commissionMainNoticePrompt(item dispatch.MainNotice) (string, error) {
	rules, err := localHeartbeatSkillsAt(w.binding.SkillsDir, w.binding.Host)
	if err != nil {
		return "", err
	}
	rule, err := skills.ReadSignedFile(rules.SkillsDir, "ef-commission", "references/main-notice.md", 64<<10)
	if err != nil {
		return "", err
	}
	payload := map[string]any{"notification_id": item.ID, "order_id": item.OrderID, "observed_order_version": item.Version, "role": item.Role, "event_kind": item.Event, "observed_state": item.State, "agent_id": w.binding.AgentID, "server": w.binding.Server}
	if item.IsLocalBlocker() {
		payload["local_execution"] = map[string]string{"job_id": item.JobID, "status": item.JobStatus, "code": item.JobCode}
	}
	if item.IsBuyerInputRequest() {
		payload["input_request_id"] = item.InputRequestID
		target, err := orderDesktopURL(w.server.Endpoint, w.binding.AgentID, item.OrderID, "buyer")
		if err != nil {
			return "", err
		}
		payload["order_url"] = target
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	prefix, err := nativeHeartbeatCLIPrefix(w.home, w.server.Name, "auto")
	if err != nil {
		return "", err
	}
	return "ACCOUNT ROUTING: use this exact CLI prefix for every Order read: " + prefix + ". Verify the observed Agent ID matches the event recipient before reporting.\n" + string(rule) + "\n\nUNTRUSTED COMMISSION EVENT:\n" + string(data), nil
}

func (w *accountWatch) dispatchCommissionMainNotice(ctx context.Context, item dispatch.MainNotice) (status, code string) {
	started := time.Now()
	observation := mainNoticeObservation(item)
	if w.binding != nil && w.binding.Host == "codex" {
		observation.Adapter = "codex_turn"
	}
	defer func() {
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
		if !w.mainNoticeRetryAllowed(ctx) {
			return "skipped", "main_notice_retry_permission_unavailable"
		}
		if !item.IsLocalBlocker() && !item.IsBuyerInputRequest() {
			var order commissionIntakeOrder
			var err error
			if item.Role == "buyer" {
				order, err = w.readBuyerClarificationOrder(ctx, item.OrderID)
			} else {
				order, err = w.readCommissionOrder(ctx, item.OrderID)
			}
			if err != nil {
				return w.retryCommissionMainNotice(ctx, "main_notice_order_unavailable")
			}
			if order.Version != item.Version || item.State != "" && order.State != item.State {
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
			return w.retryCommissionMainNotice(ctx, "main_notice_blocker_unavailable")
		}
		if order.Version != item.Version || order.State != "awaiting_seller" && order.State != "in_progress" {
			return "skipped", "main_notice_blocker_obsolete"
		}
	}
	if item.IsBuyerInputRequest() && !w.buyerInputNoticeCurrent(ctx, item) {
		return "skipped", "main_notice_input_request_changed"
	}
	prompt, err := w.commissionMainNoticePrompt(item)
	if err != nil || w.runMainNotice == nil {
		return w.retryCommissionMainNotice(ctx, "main_notice_unavailable")
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
	result, runErr := w.runMainNotice(callCtx, prompt)
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
		return w.retryCommissionMainNotice(ctx, code)
	}
	return "skipped", "main_notice_unconfirmed"
}

// Check reads OS permission only; retries never request permission or display UI.
func (w *accountWatch) mainNoticeRetryAllowed(ctx context.Context) bool {
	if w.desktop == nil || w.desktopDisabledReason != "" || ctx.Err() != nil || w.commissionIdentityOK() != nil {
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

func (w *accountWatch) retryCommissionMainNotice(ctx context.Context, code string) (string, string) {
	if w.commissionIdentityOK() != nil {
		return "skipped", "identity_changed"
	}
	if w.mainNoticeRetryAllowed(ctx) {
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
