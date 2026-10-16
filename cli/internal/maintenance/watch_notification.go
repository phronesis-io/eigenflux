package maintenance

import (
	"fmt"
	"strconv"
	"strings"
)

type WatchNotification struct {
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

func validateWatchNotification(e Event) error {
	n, w := e.Notification, e.Notification.Watch
	if w == nil || e.Trigger != "auto" || e.Source != "watch" || !notificationHash.MatchString(n.IDHash) || n.DeviceID != "" || n.DeviceName != "" || n.HResult != "" {
		return fmt.Errorf("invalid watch notification category")
	}
	if !watchOneOf(w.Origin, "order_event", "local_blocker", "legacy") || !canonicalPositiveNotificationID(w.OrderID) || !watchOneOf(w.Role, "buyer", "seller") || w.OrderVersion < 0 || w.Origin != "legacy" && w.OrderVersion == 0 || w.Origin == "legacy" && w.OrderVersion != 0 {
		return fmt.Errorf("invalid watch notification order")
	}
	if w.Origin == "order_event" {
		if !canonicalPositiveNotificationID(w.SourceNotificationID) || w.JobID != "" || w.JobKind != "" || w.JobStatus != "" || w.JobCode != "" {
			return fmt.Errorf("invalid watch order event correlation")
		}
	} else if w.Origin == "local_blocker" {
		if w.SourceNotificationID != "" || !notificationHash.MatchString(w.JobID) || !watchOneOf(w.JobKind, "commission_order", "commission_fulfillment") || !watchOneOf(w.JobStatus, "unknown", "failed", "needs_user") || !validWatchNotificationJobCode(w.JobCode) {
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
		if e.Result != "failed" || !watchOneOf(e.ErrorCode, "timeout", "cancelled", "permission_denied", "permission_required", "helper_missing", "unsupported_platform", "pending_first_notification", "setting_unavailable", "provider_failed") {
			return fmt.Errorf("invalid failed watch notification result")
		}
	default:
		return fmt.Errorf("invalid watch notification stage")
	}
	if n.CLICommit != "" && !notificationCommit.MatchString(n.CLICommit) || !safeValue.MatchString(e.Host) || !safeValue.MatchString(e.FromVersion) || !watchOneOf(e.Mode, "", "skill", "plugin") || e.DurationMS < 0 || e.DurationMS > 60000 || e.ToVersion != "" || e.RunningVersion != "" {
		return fmt.Errorf("invalid watch notification metadata")
	}
	return nil
}

func canonicalPositiveNotificationID(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

func validWatchNotificationJobCode(code string) bool {
	return watchOneOf(code, strings.Fields(`interrupted_execution identity_changed invalid_commission_notification
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

func watchOneOf(value string, values ...string) bool { return contains(values, value) }

func WatchJobCode(code string) string {
	if validWatchNotificationJobCode(code) {
		return code
	}
	return "execution_blocked"
}
