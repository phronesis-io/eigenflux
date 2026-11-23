package maintenance

import (
	"fmt"
	"strings"
)

// Fixed local codes remain separate from the stable coarse execution category.
const CommissionLocalCodes = `commission_accept_receipt_invalid commission_accept_rejected commission_accept_unconfirmed commission_buyer_notification_recorded
commission_clarification_answered commission_clarification_invalid commission_clarification_materials_unavailable commission_clarification_not_actionable
commission_clarification_order_changed commission_clarification_queued commission_clarification_result_invalid commission_clarification_rounds_exhausted
commission_clarification_rules_unavailable commission_clarification_unconfirmed commission_clarification_waiting_buyer commission_clarification_waiting_facts
commission_clarification_waiting_files commission_contract_invalid commission_credential_lock_unavailable commission_delivery_confirmed
commission_delivery_order_unavailable commission_delivery_rejected commission_delivery_retry_exhausted commission_delivery_retry_pending
commission_delivery_sending commission_delivery_unconfirmed commission_delivery_upload_unconfirmed commission_delivery_verification_unavailable
commission_fulfillment_already_queued commission_fulfillment_artifact_mismatch commission_fulfillment_artifacts_invalid commission_fulfillment_artifacts_ready
commission_fulfillment_cancelled_before_execution commission_fulfillment_directory_not_preparable commission_fulfillment_failed commission_fulfillment_inputs_changed
commission_fulfillment_job_invalid commission_fulfillment_needs_input commission_fulfillment_needs_user commission_fulfillment_order_changed
commission_fulfillment_permission_required commission_fulfillment_preflight_required commission_fulfillment_prepared commission_fulfillment_prompt_unavailable
commission_fulfillment_result_invalid commission_fulfillment_skill_unavailable commission_fulfillment_unconfirmed commission_input_request_unconfirmed
commission_intake_already_checked commission_intake_cancelled_before_execution commission_intake_evidence_missing commission_intake_failed
commission_intake_needs_input commission_intake_needs_user commission_intake_permission_required commission_intake_prompt_unavailable
commission_intake_ready commission_intake_ready_reused commission_intake_result_invalid commission_intake_unconfirmed
commission_local_check_cancelled commission_local_check_unavailable commission_local_io_unavailable commission_local_permission_denied
commission_local_state_missing commission_materials_invalid commission_materials_limit commission_materials_missing
commission_materials_unavailable commission_order_changed commission_order_not_actionable commission_order_unavailable
commission_output_directory_unavailable commission_readiness_capacity_exceeded commission_result_notification_recorded commission_seller_mismatch
commission_skill_invalid commission_skill_missing commission_skill_read_scope_invalid commission_skill_unreadable
commission_supplement_unconfirmed commission_waiting_for_payment configuration_changed execution_blocked
execution_unconfirmed identity_changed interrupted_execution invalid_commission_notification
main_notice_blocker_obsolete main_notice_blocker_resolved main_notice_blocker_unavailable main_notice_confirmed
main_notice_input_request_changed main_notice_not_submitted main_notice_order_changed main_notice_order_unavailable
main_notice_queue_full main_notice_retry_exhausted main_notice_retry_permission_unavailable main_notice_target_busy
main_notice_unavailable main_notice_unconfirmed operator_retry operator_verified
unknown_failure windows_command_line_too_long`
const CommissionBridgeCodes = `acp_extension_required acp_invalid_encoding acp_invalid_message acp_invalid_session
acp_invalid_update acp_missing_result acp_pipe_failed acp_protocol_version_mismatch
acp_read_failed_or_message_too_large acp_response_id_mismatch acp_rpc_error acp_turn_incomplete
acp_unexpected_exit acp_write_failed cancelled empty_agent_result
hermes_main_backend_changed hermes_main_busy hermes_main_connection_closed hermes_main_connection_failed
hermes_main_invalid_client_request hermes_main_invalid_config hermes_main_invalid_frame hermes_main_invalid_token_file
hermes_main_requires_loopback hermes_main_requires_user hermes_main_response_unconfirmed hermes_main_rpc_failed
hermes_main_session_unavailable hermes_main_target_mismatch hermes_main_token_unavailable hermes_main_unsupported_method
hermes_main_unsupported_protocol hermes_main_write_failed main_notice_completion_unconfirmed main_notice_hidden_input_unverified
main_notice_host_unverified main_notice_id_unavailable main_notice_missing_result main_notice_not_running
main_notice_not_submitted main_notice_owner_binding_invalid main_notice_owner_capability_unverified main_notice_owner_deadline_failed
main_notice_owner_discovery_invalid main_notice_owner_home_invalid main_notice_owner_initialize_invalid main_notice_owner_ipc_unavailable
main_notice_owner_ipc_unsupported main_notice_owner_message_invalid main_notice_owner_message_unsupported main_notice_owner_peer_mismatch
main_notice_owner_request_failed main_notice_owner_request_id_failed main_notice_owner_request_invalid main_notice_owner_request_mismatch
main_notice_owner_response_mismatch main_notice_owner_response_unconfirmed main_notice_owner_socket_untrusted main_notice_owner_unavailable
main_notice_owner_write_unconfirmed main_notice_proxy_closed_or_oversize main_notice_queue_full main_notice_reader_unavailable
main_notice_receipts_invalid main_notice_response_mismatch main_notice_response_unconfirmed main_notice_retry_exhausted
main_notice_rpc_failed main_notice_target_busy main_notice_target_invalid main_notice_target_unavailable
main_notice_transport_unverified main_notice_turn_unconfirmed main_notice_visible_input_unexpected main_notice_write_failed
main_session_mismatch main_session_unavailable output_limit_exceeded permission_denied
permission_required runner_command_required runner_current_cli_required runner_current_cli_unavailable
runner_exit_failed runner_output_too_large runner_start_failed timeout
unknown_failure workbuddy_agent_rejected workbuddy_busy workbuddy_cli_unavailable
workbuddy_config_unavailable workbuddy_connect_failed workbuddy_existing_conversation_not_supported workbuddy_home_unavailable
workbuddy_host_invalid_endpoint workbuddy_host_invalid_response workbuddy_host_invalid_runtime workbuddy_host_not_running
workbuddy_host_pid_invalid workbuddy_host_pid_unreadable workbuddy_host_request_failed workbuddy_host_response_missing
workbuddy_host_status_unknown workbuddy_host_unavailable workbuddy_host_unsupported_platform workbuddy_host_write_failed
workbuddy_http_failed workbuddy_http_rejected workbuddy_invalid_cli workbuddy_invalid_cwd
workbuddy_invalid_frame workbuddy_invalid_node workbuddy_invalid_response workbuddy_invalid_session
workbuddy_invalid_tool_name workbuddy_invalid_tools workbuddy_method_not_supported workbuddy_node_unavailable
workbuddy_random_failed workbuddy_response_missing workbuddy_session_already_created workbuddy_sidecar_not_ready
workbuddy_unique_host_unavailable workbuddy_unsupported_agent_request workbuddy_untrusted_pipe_peer workbuddy_untrusted_socket`

type CommissionCheckReceipt struct {
	Status              string `json:"status"`
	Outcome             string `json:"outcome,omitempty"`
	CheckedOrderVersion int64  `json:"checked_order_version,omitempty"`
	InspectedFileCount  *int   `json:"inspected_file_count,omitempty"`
	InputFingerprint    string `json:"input_fingerprint,omitempty"`
	SourceJobID         string `json:"source_job_id,omitempty"`
}

type CommissionBridgeError struct {
	Code      string `json:"code"`
	Message   string `json:"message,omitempty"`
	Operation string `json:"operation,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	RPCCode   *int   `json:"rpc_code,omitempty"`
}

func CommissionLocalCode(code string) string {
	if watchOneOf(code, strings.Fields(CommissionLocalCodes)...) {
		return code
	}
	return "unknown_failure"
}
func CommissionBridgeCode(code string) string {
	if watchOneOf(code, strings.Fields(CommissionBridgeCodes)...) {
		return code
	}
	return "unknown_failure"
}
func ValidateCommissionBridgeError(b *CommissionBridgeError) error {
	if b == nil {
		return nil
	}
	if CommissionBridgeCode(b.Code) != b.Code || b.Code == "" || b.Message != "" && b.Message != b.Code || !watchOneOf(b.Operation, "", "read", "write", "execute", "network") || b.ExitCode != nil && (*b.ExitCode < -1 || *b.ExitCode > 255) || b.RPCCode != nil && !validCommissionRPCCode(*b.RPCCode) {
		return fmt.Errorf("invalid commission bridge diagnostic")
	}
	if b.Code == "unknown_failure" && (b.Message != "" || b.Operation != "" || b.ExitCode != nil || b.RPCCode != nil) {
		return fmt.Errorf("unknown bridge diagnostic cannot claim details")
	}
	return nil
}
func validCommissionRPCCode(code int) bool {
	switch code {
	case -32700, -32600, -32601, -32602, -32603, -32000:
		return true
	}
	return false
}
func ValidateCommissionCheckReceipt(r *CommissionCheckReceipt) error {
	if r == nil {
		return nil
	}
	switch r.Status {
	case "missing", "not_applicable":
		if r.Outcome != "" || r.CheckedOrderVersion != 0 || r.InspectedFileCount != nil || r.InputFingerprint != "" || r.SourceJobID != "" {
			return fmt.Errorf("absent check receipt cannot claim evidence")
		}
	case "present":
		if !watchOneOf(r.Outcome, "ready", "needs_input", "needs_user", "failed") || r.CheckedOrderVersion <= 0 || r.InspectedFileCount == nil || *r.InspectedFileCount < 0 || *r.InspectedFileCount > 128 || !notificationHash.MatchString(r.SourceJobID) || r.InputFingerprint != "" && (!notificationHash.MatchString(r.InputFingerprint) || r.Outcome != "ready") {
			return fmt.Errorf("invalid commission check receipt")
		}
	default:
		return fmt.Errorf("invalid commission check receipt status")
	}
	return nil
}
func ValidateCommissionDetails(c *Commission) error {
	if c.LocalCode != "" && CommissionLocalCode(c.LocalCode) != c.LocalCode {
		return fmt.Errorf("invalid commission local code")
	}
	if c.CheckReceipt != nil && c.CheckReceipt.Status == "present" && c.OrderVersion > 0 && c.CheckReceipt.CheckedOrderVersion > c.OrderVersion {
		return fmt.Errorf("check receipt version exceeds observation")
	}
	if err := ValidateCommissionCheckReceipt(c.CheckReceipt); err != nil {
		return err
	}
	return ValidateCommissionBridgeError(c.BridgeError)
}
