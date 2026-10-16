package consolev2

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type notificationMaintenanceData struct {
	Action     string `json:"action"`
	Visibility string `json:"visibility"`
	IDHash     string `json:"id_hash,omitempty"`
	Stage      string `json:"stage,omitempty"`
	HResult    string `json:"hresult,omitempty"`
	CLICommit  string `json:"cli_commit,omitempty"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
}

var notificationMaintenanceHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var notificationMaintenanceHResult = regexp.MustCompile(`^0x[0-9A-Fa-f]{8}$`)
var notificationMaintenanceCommit = regexp.MustCompile(`^[a-f0-9]{8,40}(-dirty)?$`)

func validateNotificationMaintenance(e maintenanceEvent) error {
	n := e.Notification
	if n == nil || e.Component != "notifier" || !maintenanceOneOf(e.Trigger, "auto", "manual") || !maintenanceOneOf(e.Source, "installer", "watch", "manual", "test") || !maintenanceOneOf(e.Platform, "windows", "darwin") || e.ReasonCategory != "" {
		return fmt.Errorf("invalid notification category")
	}
	if !maintenanceOneOf(n.Action, "setup", "status", "send") || n.Visibility != "unknown" {
		return fmt.Errorf("invalid notification action or visibility claim")
	}
	if n.DeviceID != "" || n.DeviceName != "" {
		if n.Action != "status" || !notificationDeviceID.MatchString(n.DeviceID) || !validNotificationDeviceName(n.DeviceName) {
			return fmt.Errorf("invalid notification device")
		}
	}
	if n.Action == "send" {
		if !maintenanceOneOf(e.Result, "submitted", "disabled", "failed") {
			return fmt.Errorf("invalid notification result")
		}
	} else if !maintenanceOneOf(e.Result, "ready", "registered", "disabled", "failed") {
		return fmt.Errorf("invalid notification result")
	}
	if n.Action == "send" && !notificationMaintenanceHash.MatchString(n.IDHash) || n.Action != "send" && n.IDHash != "" {
		return fmt.Errorf("invalid notification ID hash")
	}
	if !maintenanceOneOf(e.ErrorCode, "", "pending_first_notification", "permission_denied", "permission_required", "helper_missing", "unsupported_platform", "timeout", "cancelled", "setting_unavailable", "provider_failed") {
		return fmt.Errorf("invalid notification error code")
	}
	if (e.Result == "ready" || e.Result == "submitted") && e.ErrorCode != "" || e.Result == "registered" && e.ErrorCode != "pending_first_notification" || e.Result == "disabled" && e.ErrorCode != "permission_denied" || e.Result == "failed" && (e.ErrorCode == "" || e.ErrorCode == "pending_first_notification" || e.ErrorCode == "permission_denied") {
		return fmt.Errorf("notification result and code disagree")
	}
	if !maintenanceOneOf(n.Stage, "", "request", "remove", "registration", "winrt_load", "notifier_create", "setting_read", "toast_xml", "toast_create", "toast_show") || (n.Stage == "") != (n.HResult == "") || n.HResult != "" && !notificationMaintenanceHResult.MatchString(n.HResult) || n.HResult != "" && e.Result != "failed" {
		return fmt.Errorf("invalid notification helper diagnostic")
	}
	if n.CLICommit != "" && !notificationMaintenanceCommit.MatchString(n.CLICommit) || !maintenanceValue.MatchString(e.Host) || !maintenanceValue.MatchString(e.FromVersion) || !maintenanceOneOf(e.Mode, "", "skill", "plugin") || e.DurationMS < 0 || e.DurationMS > 60000 || e.ToVersion != "" || e.RunningVersion != "" {
		return fmt.Errorf("invalid notification metadata")
	}
	return nil
}

var notificationDeviceID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func validNotificationDeviceName(name string) bool {
	if !utf8.ValidString(name) || strings.TrimSpace(name) != name || name == "" || utf8.RuneCountInString(name) > 64 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return false
		}
	}
	return true
}
