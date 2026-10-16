package desktopnotify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"runtime"
	"time"
)

// Observation records submission, never banner visibility or human attention.
// It excludes notification text, URLs, credentials and raw provider errors.
type Observation struct {
	DeviceID           string    `json:"device_id,omitempty"`
	DeviceName         string    `json:"device_name,omitempty"`
	EventID            string    `json:"event_id,omitempty"`
	AgentID            string    `json:"agent_id,omitempty"`
	Source             string    `json:"source,omitempty"`
	Queued             bool      `json:"queued,omitempty"`
	CLIVersion         string    `json:"cli_version,omitempty"`
	CLICommit          string    `json:"cli_commit,omitempty"`
	At                 time.Time `json:"at"`
	Platform           string    `json:"platform"`
	Action             string    `json:"action"`
	Result             string    `json:"result"`
	Visibility         string    `json:"visibility"`
	NotificationIDHash string    `json:"notification_id_hash,omitempty"`
	DurationMS         int64     `json:"duration_ms"`
	Code               string    `json:"code,omitempty"`
	Stage              string    `json:"stage,omitempty"`
	HResult            string    `json:"hresult,omitempty"`
}

func (n *Notifier) request(ctx context.Context, action string, message Message) error {
	start := time.Now()
	err := platformRequest(ctx, action, message)
	if n.observe != nil {
		n.observe(observeResult(action, message, err, start))
	}
	return err
}

func observeResult(action string, message Message, err error, start time.Time) Observation {
	a := map[string]string{"enable": "setup", "check": "status", "show": "send"}[action]
	e := Observation{At: time.Now().UTC(), Platform: runtime.GOOS, Action: a, Result: "ready", Visibility: "unknown", DurationMS: time.Since(start).Milliseconds()}
	if action == "show" {
		e.Result = "submitted"
		digest := sha256.Sum256([]byte(message.ID))
		e.NotificationIDHash = hex.EncodeToString(digest[:])
	}
	if err == nil {
		return e
	}
	e.Result = "failed"
	switch {
	case errors.Is(err, ErrPendingFirstNotification):
		e.Result, e.Code = "registered", "pending_first_notification"
	case errors.Is(err, ErrPermission):
		e.Result, e.Code = "disabled", "permission_denied"
	case errors.Is(err, ErrPermissionRequired):
		e.Code = "permission_required"
	case errors.Is(err, ErrHelperMissing):
		e.Code = "helper_missing"
	case errors.Is(err, ErrUnsupported):
		e.Code = "unsupported_platform"
	case errors.Is(err, context.DeadlineExceeded):
		e.Code = "timeout"
	case errors.Is(err, context.Canceled):
		e.Code = "cancelled"
	case errors.Is(err, errWindowsSettingUnavailable):
		e.Code = "setting_unavailable"
	default:
		e.Code = "provider_failed"
		var helper *WindowsHelperError
		if errors.As(err, &helper) {
			e.Stage, e.HResult = helper.Stage, helper.HResult
		}
	}
	return e
}
