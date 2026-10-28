package consolev2

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type notificationMaintenanceData struct {
	Action     string                            `json:"action"`
	Visibility string                            `json:"visibility"`
	IDHash     string                            `json:"id_hash,omitempty"`
	Stage      string                            `json:"stage,omitempty"`
	HResult    string                            `json:"hresult,omitempty"`
	CLICommit  string                            `json:"cli_commit,omitempty"`
	DeviceID   string                            `json:"device_id,omitempty"`
	DeviceName string                            `json:"device_name,omitempty"`
	Watch      *watchNotificationMaintenanceData `json:"watch,omitempty"`
}

type watchNotificationMaintenanceData struct {
	Origin               string `json:"origin"`
	OrderID              string `json:"order_id"`
	OrderVersion         int64  `json:"order_version"`
	Role                 string `json:"role"`
	SourceNotificationID string `json:"source_notification_id,omitempty"`
	JobID                string `json:"job_id,omitempty"`
	JobKind              string `json:"job_kind,omitempty"`
	JobStatus            string `json:"job_status,omitempty"`
	JobCode              string `json:"job_code,omitempty"`
	Attempt              int    `json:"attempt"`
}

var notificationMaintenanceHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var notificationMaintenanceHResult = regexp.MustCompile(`^0x[0-9A-Fa-f]{8}$`)
var notificationMaintenanceCommit = regexp.MustCompile(`^[a-f0-9]{8,40}(-dirty)?$`)

func validateNotificationMaintenance(e maintenanceEvent) error {
	n := e.Notification
	if n == nil || e.Component != "notifier" || !maintenanceOneOf(e.Trigger, "auto", "manual") || !maintenanceOneOf(e.Source, "installer", "watch", "manual", "test") || !maintenanceOneOf(e.Platform, "windows", "darwin") || e.ReasonCategory != "" {
		return fmt.Errorf("invalid notification category")
	}
	if !maintenanceOneOf(n.Action, "setup", "status", "send", "watch") || n.Visibility != "unknown" {
		return fmt.Errorf("invalid notification action or visibility claim")
	}
	if n.Action == "watch" {
		return validateWatchNotificationMaintenance(e)
	}
	if n.Watch != nil {
		return fmt.Errorf("watch metadata requires watch action")
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

func validateWatchNotificationMaintenance(e maintenanceEvent) error {
	n, w := e.Notification, e.Notification.Watch
	if w == nil || e.Trigger != "auto" || e.Source != "watch" || !notificationMaintenanceHash.MatchString(n.IDHash) || n.DeviceID != "" || n.DeviceName != "" || n.HResult != "" {
		return fmt.Errorf("invalid watch notification category")
	}
	if !maintenanceOneOf(w.Origin, "order_event", "local_blocker", "legacy") || !canonicalPositiveNotificationID(w.OrderID) || !maintenanceOneOf(w.Role, "buyer", "seller") || w.OrderVersion < 0 || w.Origin != "legacy" && w.OrderVersion == 0 || w.Origin == "legacy" && w.OrderVersion != 0 {
		return fmt.Errorf("invalid watch notification order")
	}
	if w.Origin == "order_event" {
		if !canonicalPositiveNotificationID(w.SourceNotificationID) || w.JobID != "" || w.JobKind != "" || w.JobStatus != "" || w.JobCode != "" {
			return fmt.Errorf("invalid watch order event correlation")
		}
	} else if w.Origin == "local_blocker" {
		if w.SourceNotificationID != "" || !notificationMaintenanceHash.MatchString(w.JobID) || !maintenanceOneOf(w.JobKind, "commission_order", "commission_fulfillment") || !maintenanceOneOf(w.JobStatus, "unknown", "failed", "needs_user") || !validWatchNotificationJobCode(w.JobCode) {
			return fmt.Errorf("invalid watch blocker correlation")
		}
	} else if w.SourceNotificationID != "" || w.JobID != "" || w.JobKind != "" || w.JobStatus != "" || w.JobCode != "" {
		return fmt.Errorf("invalid legacy watch correlation")
	}
	if w.Attempt < 0 || n.Stage == "queued" && w.Attempt != 0 || n.Stage != "queued" && w.Attempt < 1 {
		return fmt.Errorf("invalid watch notification attempt")
	}
	switch n.Stage {
	case "queued":
		if e.Result != "queued" || e.ErrorCode != "" {
			return fmt.Errorf("invalid queued notification result")
		}
	case "os_submitted":
		if e.Result != "submitted" || e.ErrorCode != "" {
			return fmt.Errorf("invalid submitted notification result")
		}
	case "prepare_failed":
		if e.Result != "failed" || e.ErrorCode != "service_name_unavailable" {
			return fmt.Errorf("invalid failed watch notification preparation")
		}
	case "os_failed":
		if e.Result != "failed" || !maintenanceOneOf(e.ErrorCode, "timeout", "cancelled", "permission_denied", "permission_required", "helper_missing", "unsupported_platform", "pending_first_notification", "setting_unavailable", "provider_failed") {
			return fmt.Errorf("invalid failed watch notification result")
		}
	default:
		return fmt.Errorf("invalid watch notification stage")
	}
	if n.CLICommit != "" && !notificationMaintenanceCommit.MatchString(n.CLICommit) || !maintenanceValue.MatchString(e.Host) || !maintenanceValue.MatchString(e.FromVersion) || !maintenanceOneOf(e.Mode, "", "skill", "plugin") || e.DurationMS < 0 || e.DurationMS > 60000 || e.ToVersion != "" || e.RunningVersion != "" {
		return fmt.Errorf("invalid watch notification metadata")
	}
	return nil
}

func canonicalPositiveNotificationID(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

func validWatchNotificationJobCode(code string) bool {
	return maintenanceOneOf(code, strings.Fields(`interrupted_execution identity_changed invalid_commission_notification
commission_accept_receipt_invalid commission_accept_rejected commission_accept_unconfirmed commission_contract_invalid
commission_delivery_order_unavailable commission_delivery_unconfirmed commission_fulfillment_artifact_mismatch
commission_fulfillment_artifacts_invalid commission_fulfillment_cancelled_before_execution commission_fulfillment_directory_not_preparable
commission_fulfillment_failed commission_fulfillment_inputs_changed commission_fulfillment_needs_input commission_fulfillment_order_changed
commission_fulfillment_permission_required commission_fulfillment_prompt_unavailable commission_fulfillment_result_invalid
commission_fulfillment_skill_unavailable commission_fulfillment_unconfirmed commission_intake_cancelled_before_execution
commission_intake_needs_input commission_intake_permission_required commission_intake_prompt_unavailable commission_intake_result_invalid
commission_intake_unconfirmed commission_materials_invalid commission_materials_limit commission_materials_missing
commission_materials_unavailable commission_order_changed commission_order_unavailable commission_output_directory_unavailable
commission_seller_mismatch commission_skill_invalid commission_skill_missing commission_skill_unreadable execution_blocked`)...)
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
