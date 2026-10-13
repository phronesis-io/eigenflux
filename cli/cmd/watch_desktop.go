package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
		orderNotificationPayload
		AgentID notificationID `json:"recipient_agent_id"`
	} `json:"payload"`
}

func commissionDesktopMessage(raw json.RawMessage, endpoint, agentID, language string) (desktopnotify.Message, error) {
	var fact desktopOrderFact
	if json.Unmarshal(raw, &fact) != nil || fact.ID <= 0 || fact.Source != "commission_order" || strconv.FormatInt(int64(fact.Payload.AgentID), 10) != agentID {
		return desktopnotify.Message{}, errors.New("invalid_desktop_commission_identity")
	}
	p := fact.Payload
	orderID := strconv.FormatInt(int64(p.OrderID), 10)
	target, err := orderDesktopURL(endpoint, agentID, orderID, p.RecipientRole)
	if err != nil {
		return desktopnotify.Message{}, err
	}
	title, body := commissionDesktopCopy(language, fact.Type, p.orderNotificationPayload)
	return desktopnotify.Message{ID: "commission:" + strconv.FormatInt(int64(fact.ID), 10), Title: title, Body: body, URL: target}, nil
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
	m, err := commissionDesktopMessage(raw, w.server.Endpoint, w.identity.AgentID, desktopnotify.SystemLanguage())
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
		title, body := desktopBlockerText(desktopnotify.SystemLanguage(), job.Status)
		m := desktopnotify.Message{ID: "local:" + hex.EncodeToString(sum[:]), Title: title, Body: body, URL: target}
		if err := w.desktop.Enqueue(m); err != nil {
			return err
		}
	}
	return nil
}

func desktopBlockerText(language, status string) (string, string) {
	return desktopCopyText(language, "seller", "local."+status)
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
			if err := w.emit("desktop_notification_status", desktopSetupStatus(err)); err != nil {
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
				message, err := w.resolveDesktopServiceName(call, entry.Message)
				if identityErr := w.dispatchIdentityOK(); identityErr != nil {
					cancel()
					return identityErr
				}
				if err == nil {
					err = w.desktopProvider.Show(call, message)
				}
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
				data := map[string]string{"notification_id": entry.Message.ID, "status": status}
				if errors.Is(err, errDesktopServiceName) {
					data["code"] = "service_name_unavailable"
				}
				if err := w.emit("desktop_notification_status", data); err != nil {
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

// Fixed diagnostics keep setup failures actionable without exposing provider data.
func desktopSetupStatus(err error) map[string]string {
	if err == nil {
		return map[string]string{"status": "ready"}
	}
	code, action := "setup_failed", "run_notify_setup"
	switch {
	case errors.Is(err, desktopnotify.ErrHelperMissing):
		code, action = "helper_missing", "install_complete_bundle"
	case errors.Is(err, desktopnotify.ErrPermissionRequired):
		code = "permission_required"
	case errors.Is(err, desktopnotify.ErrPermission):
		code, action = "permission_denied", "open_system_notification_settings"
	case errors.Is(err, context.DeadlineExceeded):
		code, action = "setup_timeout", "check_system_permission_dialog"
	case errors.Is(err, desktopnotify.ErrUnsupported):
		code, action = "unsupported_platform", "use_macos_or_windows"
	}
	return map[string]string{"status": "permission_or_setup_required", "code": code, "action": action}
}
func nativeDesktopSupported() bool { return runtime.GOOS == "darwin" || runtime.GOOS == "windows" }
