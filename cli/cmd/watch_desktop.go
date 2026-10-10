package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/desktopqueue"
)

type desktopProvider interface {
	Setup(context.Context) error
	Show(context.Context, desktopnotify.Message) error
}

func orderDesktopURL(endpoint, agentID, orderID, role string) (string, error) {
	for _, s := range []string{agentID, orderID} {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != s {
			return "", errors.New("invalid_desktop_target")
		}
	}
	if role != "buyer" && role != "seller" {
		return "", errors.New("invalid_desktop_role")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid_desktop_origin")
	}
	u.Path, u.RawPath = "/dashboard/notifications/open", ""
	u.RawQuery = url.Values{"agent_id": {agentID}, "order_id": {orderID}, "role": {role}}.Encode()
	if err := desktopnotify.ValidateURL(u.String()); err != nil {
		return "", err
	}
	return u.String(), nil
}

type desktopOrderFact struct {
	ID      notificationID `json:"notification_id"`
	Type    string         `json:"type"`
	Source  string         `json:"source_type"`
	Payload struct {
		OrderID notificationID `json:"order_id"`
		AgentID notificationID `json:"recipient_agent_id"`
		Role    string         `json:"recipient_role"`
		State   string         `json:"to_state"`
	} `json:"payload"`
}

func commissionDesktopMessage(raw json.RawMessage, endpoint, agentID string) (desktopnotify.Message, error) {
	var fact desktopOrderFact
	if json.Unmarshal(raw, &fact) != nil || fact.ID <= 0 || fact.Source != "commission_order" || strconv.FormatInt(int64(fact.Payload.AgentID), 10) != agentID {
		return desktopnotify.Message{}, errors.New("invalid_desktop_commission_identity")
	}
	p := fact.Payload
	orderID := strconv.FormatInt(int64(p.OrderID), 10)
	target, err := orderDesktopURL(endpoint, agentID, orderID, p.Role)
	if err != nil {
		return desktopnotify.Message{}, err
	}
	role, summary := localizedOrderNotification("zh", p.Role, p.State)
	if fact.Type == "order.reminder.buyer_confirmation_24h.v1" {
		summary = "请及时确认交付结果"
	}
	return desktopnotify.Message{ID: "commission:" + strconv.FormatInt(int64(fact.ID), 10), Title: "任务委托 · " + summary, Body: fmt.Sprintf("%s · 订单 %s · 账号 %s", role, orderID, agentID), URL: target}, nil
}
func (w *accountWatch) enableDesktop() error {
	if !w.receivesCommission() {
		return nil
	}
	if _, err := orderDesktopURL(w.server.Endpoint, w.identity.AgentID, "1", "seller"); err != nil {
		w.desktopDisabledReason = "unsupported_console_origin"
		return nil
	}
	q, err := desktopqueue.Open(filepath.Join(w.home, "watch", "desktop-"+w.scope+".json"), w.scope)
	if err != nil {
		return err
	}
	w.desktop = q
	w.desktopProvider = desktopnotify.New()
	return nil
}
func (w *accountWatch) enqueueDesktopCommission(raw json.RawMessage) error {
	if w.desktop == nil {
		return nil
	}
	m, err := commissionDesktopMessage(raw, w.server.Endpoint, w.identity.AgentID)
	if err != nil {
		return err
	}
	m.ID = w.scope + ":" + m.ID
	return w.desktop.Enqueue(m)
}

// Recover unresolved local failures from durable execution state, including after a crash.
func (w *accountWatch) enqueueDesktopBlockers() error {
	if w.desktop == nil || w.journal == nil {
		return nil
	}
	blockers, err := w.journal.CommissionBlockers()
	if err != nil {
		return err
	}
	for _, job := range blockers {
		orderID, role := job.OrderID, job.Role
		target, err := orderDesktopURL(w.server.Endpoint, w.identity.AgentID, orderID, role)
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(w.scope + ":" + job.ID + ":" + job.Status + ":" + job.Code))
		summary := map[string]string{"needs_user": "需要你处理", "failed": "自动处理失败", "unknown": "处理结果需要核对"}[job.Status]
		m := desktopnotify.Message{ID: "local:" + hex.EncodeToString(sum[:]), Title: "任务委托 · " + summary, Body: "订单 " + orderID + " · 请打开控制台核对，或查看外循环处理记录。", URL: target}
		if err := w.desktop.Enqueue(m); err != nil {
			return err
		}
	}
	return nil
}
func (w *accountWatch) desktopLoop(ctx context.Context) error {
	if w.desktopDisabledReason != "" {
		return w.emit("desktop_notification_status", map[string]string{"status": "unavailable", "code": w.desktopDisabledReason})
	}
	if w.desktop == nil || w.desktopProvider == nil {
		return nil
	}
	ready := false
	retrySetup := time.Time{}
	for ctx.Err() == nil {
		if err := w.dispatchIdentityOK(); err != nil {
			return err
		}
		if err := w.enqueueDesktopBlockers(); err != nil {
			return errors.Join(errWatchDispatchJournal, err)
		}
		if !ready && !time.Now().Before(retrySetup) {
			call, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := w.desktopProvider.Setup(call)
			cancel()
			ready = err == nil
			retrySetup = time.Now().Add(5 * time.Minute)
			status := "ready"
			if err != nil {
				status = "permission_or_setup_required"
			}
			if err := w.emit("desktop_notification_status", map[string]string{"status": status}); err != nil {
				return err
			}
		}
		if ready {
			entry, ok := w.desktop.Next(time.Now())
			if ok {
				if err := w.dispatchIdentityOK(); err != nil {
					return err
				}
				call, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := w.desktopProvider.Show(call, entry.Message)
				cancel()
				if err := w.dispatchIdentityOK(); err != nil {
					return err
				}
				if saveErr := w.desktop.Complete(entry.Message.ID, err == nil, time.Now()); saveErr != nil {
					return errors.Join(errWatchDispatchJournal, saveErr)
				}
				status := "submitted"
				if err != nil {
					status = "retry_pending"
				}
				if err := w.emit("desktop_notification_status", map[string]string{"notification_id": entry.Message.ID, "status": status}); err != nil {
					return err
				}
			}
		}
		// A separate lane and bounded delivery rate keep OS UI off Socket/Agent paths.
		if !watchPause(ctx, 3*time.Second) {
			break
		}
	}
	return ctx.Err()
}
func nativeDesktopSupported() bool { return runtime.GOOS == "darwin" || runtime.GOOS == "windows" }
