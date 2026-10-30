package consolev2

import (
	"fmt"
	"strings"
	"time"
)

// This is diagnostic evidence, not an authoritative order-state transition.
type commissionMaintenanceData struct {
	OrderID              string `json:"order_id"`
	OrderVersion         int64  `json:"order_version"`
	Role                 string `json:"role"`
	JobID                string `json:"job_id"`
	JobKind              string `json:"job_kind"`
	SourceNotificationID string `json:"source_notification_id,omitempty"`
	Stage                string `json:"stage"`
	Attempt              int    `json:"attempt"`
	NextAttemptAt        int64  `json:"next_attempt_at,omitempty"`
	CLICommit            string `json:"cli_commit,omitempty"`
	Adapter              string `json:"adapter,omitempty"`
	Transport            string `json:"transport,omitempty"`
	HostSessionIDHash    string `json:"host_session_id_hash,omitempty"`
	HostRunIDHash        string `json:"host_run_id_hash,omitempty"`
	ArtifactIDHash       string `json:"artifact_id_hash,omitempty"`
	Operation            string `json:"operation,omitempty"`
	Resource             string `json:"resource,omitempty"`
	HTTPStatus           int    `json:"http_status,omitempty"`
	InputRequestIDHash   string `json:"input_request_id_hash,omitempty"`
	InputRound           int    `json:"input_round,omitempty"`
}

func validateCommissionMaintenance(e maintenanceEvent) error {
	c := e.Commission
	if c == nil || e.Component != "dispatch" || e.Phase != "commission" || e.Trigger != "auto" || e.Source != "watch" || e.Notification != nil || e.ReasonCategory != "" {
		return fmt.Errorf("invalid commission diagnostic category")
	}
	if !maintenanceOneOf(e.Platform, "darwin", "windows", "linux") || !maintenanceValue.MatchString(e.Host) || !maintenanceValue.MatchString(e.FromVersion) || !maintenanceOneOf(e.Mode, "", "skill", "plugin") || e.ToVersion != "" || e.RunningVersion != "" || e.DurationMS < 0 || e.DurationMS > int64(24*time.Hour/time.Millisecond) {
		return fmt.Errorf("invalid commission diagnostic metadata")
	}
	if !canonicalPositiveNotificationID(c.OrderID) || c.OrderVersion < 0 || c.OrderVersion == 0 && c.Stage != "preflight" || !maintenanceOneOf(c.Role, "buyer", "seller") || !notificationMaintenanceHash.MatchString(c.JobID) || !maintenanceOneOf(c.JobKind, "commission_order", "commission_fulfillment", "commission_clarification", "main_notice", "material_notice") || c.SourceNotificationID != "" && !canonicalPositiveNotificationID(c.SourceNotificationID) {
		return fmt.Errorf("invalid commission diagnostic correlation")
	}
	if !maintenanceOneOf(c.Stage, "queued", "claimed", "preflight", "agent", "artifact", "upload_grant", "upload_put", "upload_confirm", "deliver", "verify", "main_notice", "input_check", "accept", "job_finished", "retry") || !maintenanceOneOf(e.Result, "queued", "started", "succeeded", "failed", "blocked", "retry_scheduled", "skipped") {
		return fmt.Errorf("invalid commission diagnostic stage or result")
	}
	if c.Attempt < 0 || c.Attempt > 5 || c.Attempt == 0 && c.Stage != "queued" || (c.Stage == "queued") != (e.Result == "queued") {
		return fmt.Errorf("invalid commission diagnostic attempt")
	}
	if e.Result == "retry_scheduled" {
		if c.Attempt >= 5 || c.NextAttemptAt <= e.EventAt || c.NextAttemptAt > e.EventAt+int64(7*24*time.Hour/time.Millisecond) {
			return fmt.Errorf("invalid commission retry schedule")
		}
	} else if c.NextAttemptAt != 0 {
		return fmt.Errorf("retry time requires scheduled retry")
	}
	if e.ErrorCode != "" && !validCommissionMaintenanceCode(e.ErrorCode) || maintenanceOneOf(e.Result, "queued", "started", "succeeded") && e.ErrorCode != "" || maintenanceOneOf(e.Result, "failed", "blocked", "retry_scheduled") && e.ErrorCode == "" {
		return fmt.Errorf("invalid commission diagnostic error code")
	}
	if !maintenanceOneOf(c.Transport, "", "socket", "http_poll", "local") {
		return fmt.Errorf("invalid commission diagnostic transport")
	}
	if c.CLICommit != "" && !notificationMaintenanceCommit.MatchString(c.CLICommit) || !maintenanceOneOf(c.Adapter, "", "acp", "codex_turn", "workbuddy_bridge", "command", "native") || !maintenanceOneOf(c.Operation, "", "read", "write", "execute", "network") || !maintenanceOneOf(c.Resource, "", "skill", "workspace", "credentials", "host") || c.HTTPStatus != 0 && (c.HTTPStatus < 100 || c.HTTPStatus > 599) {
		return fmt.Errorf("invalid commission diagnostic details")
	}
	for _, hash := range []string{c.HostSessionIDHash, c.HostRunIDHash, c.ArtifactIDHash, c.InputRequestIDHash} {
		if hash != "" && !notificationMaintenanceHash.MatchString(hash) {
			return fmt.Errorf("invalid commission diagnostic hash")
		}
	}
	if c.InputRound < 0 || c.InputRound > 3 || c.InputRound > 0 && c.InputRequestIDHash == "" || c.InputRequestIDHash != "" && c.InputRound == 0 && c.JobKind != "main_notice" {
		return fmt.Errorf("invalid commission input request correlation")
	}
	return nil
}

func validCommissionMaintenanceCode(code string) bool {
	return maintenanceOneOf(code, strings.Fields(`skill_missing skill_unreadable skill_invalid contract_invalid
permission_required permission_denied materials_missing materials_invalid materials_unavailable inputs_changed order_changed
output_unavailable execution_unconfirmed execution_failed execution_blocked preflight_unavailable agent_unconfirmed
timeout cancelled runner_start_failed runner_exit_failed output_invalid remote_rejected transport_failed local_state_failed
main_notice_busy main_notice_unavailable main_notice_unconfirmed identity_changed retry_exhausted unknown_failure`)...)
}
