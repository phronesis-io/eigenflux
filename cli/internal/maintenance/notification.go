package maintenance

import (
	"errors"
	"regexp"
)

type Notification struct {
	Action     string `json:"action"`
	Visibility string `json:"visibility"`
	IDHash     string `json:"id_hash,omitempty"`
	Stage      string `json:"stage,omitempty"`
	HResult    string `json:"hresult,omitempty"`
	CLICommit  string `json:"cli_commit,omitempty"`
}

var notificationHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var notificationHResult = regexp.MustCompile(`^0x[0-9A-Fa-f]{8}$`)
var notificationCommit = regexp.MustCompile(`^[a-f0-9]{8,40}(-dirty)?$`)

func validateNotification(e Event) error {
	n := e.Notification
	if n == nil || e.Component != "notifier" || !contains([]string{"auto", "manual"}, e.Trigger) || !contains([]string{"installer", "watch", "manual", "test"}, e.Source) || !contains([]string{"windows", "darwin"}, e.Platform) || e.ReasonCategory != "" {
		return errors.New("invalid notification category")
	}
	if !contains([]string{"setup", "status", "send"}, n.Action) || n.Visibility != "unknown" {
		return errors.New("invalid notification action or visibility claim")
	}
	allowed := []string{"ready", "registered", "disabled", "failed"}
	if n.Action == "send" {
		allowed = []string{"submitted", "disabled", "failed"}
	}
	if !contains(allowed, e.Result) {
		return errors.New("invalid notification result")
	}
	if n.Action == "send" && !notificationHash.MatchString(n.IDHash) || n.Action != "send" && n.IDHash != "" {
		return errors.New("invalid notification ID hash")
	}
	if !contains([]string{"", "pending_first_notification", "permission_denied", "permission_required", "helper_missing", "unsupported_platform", "timeout", "cancelled", "setting_unavailable", "provider_failed"}, e.ErrorCode) {
		return errors.New("invalid notification error code")
	}
	if (e.Result == "ready" || e.Result == "submitted") && e.ErrorCode != "" || e.Result == "registered" && e.ErrorCode != "pending_first_notification" || e.Result == "disabled" && e.ErrorCode != "permission_denied" || e.Result == "failed" && (e.ErrorCode == "" || e.ErrorCode == "pending_first_notification" || e.ErrorCode == "permission_denied") {
		return errors.New("notification result and code disagree")
	}
	if !contains([]string{"", "request", "remove", "registration", "winrt_load", "notifier_create", "setting_read", "toast_xml", "toast_create", "toast_show"}, n.Stage) || (n.Stage == "") != (n.HResult == "") || n.HResult != "" && !notificationHResult.MatchString(n.HResult) || n.HResult != "" && e.Result != "failed" {
		return errors.New("invalid notification helper diagnostic")
	}
	if n.CLICommit != "" && !notificationCommit.MatchString(n.CLICommit) || !safeValue.MatchString(e.Host) || !safeValue.MatchString(e.FromVersion) || !contains([]string{"", "skill", "plugin"}, e.Mode) || e.DurationMS < 0 || e.DurationMS > 60000 || e.ToVersion != "" || e.RunningVersion != "" {
		return errors.New("invalid notification metadata")
	}
	return nil
}
