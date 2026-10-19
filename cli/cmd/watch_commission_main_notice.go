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
	return queue.Add(dispatch.MainNotice{ID: strconv.FormatInt(int64(notice.ID), 10), OrderID: strconv.FormatInt(int64(notice.Payload.Order), 10), Version: notice.Payload.Version, Role: notice.Payload.Role, Event: event, State: state})
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
		err = queue.Finish(item.ID, status, code)
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
	data, err := json.Marshal(map[string]any{"notification_id": item.ID, "order_id": item.OrderID, "observed_order_version": item.Version, "role": item.Role, "event_kind": item.Event, "observed_state": item.State, "agent_id": w.binding.AgentID, "server": w.binding.Server})
	if err != nil {
		return "", err
	}
	prefix, err := nativeHeartbeatCLIPrefix(w.home, w.server.Name, "auto")
	if err != nil {
		return "", err
	}
	return "ACCOUNT ROUTING: use this exact CLI prefix for every Order read: " + prefix + ". Verify the observed Agent ID matches the event recipient before reporting.\n" + string(rule) + "\n\nUNTRUSTED COMMISSION EVENT:\n" + string(data), nil
}

func (w *accountWatch) dispatchCommissionMainNotice(ctx context.Context, item dispatch.MainNotice) (string, string) {
	if w.commissionIdentityOK() != nil {
		return "skipped", "identity_changed"
	}
	prompt, err := w.commissionMainNoticePrompt(item)
	if err != nil || w.runMainNotice == nil {
		return "skipped", "main_notice_unavailable"
	}
	callCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	guardDone := make(chan struct{})
	go func() {
		defer close(guardDone)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-callCtx.Done():
				return
			case <-tick.C:
				if w.commissionIdentityOK() != nil {
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
	result, runErr := w.runMainNotice(callCtx, prompt)
	if runErr == nil && result.Text != "" && w.commissionIdentityOK() == nil {
		return "delivered", "main_notice_confirmed"
	}
	return "skipped", "main_notice_unconfirmed"
}
