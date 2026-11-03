package mainbridge

const (
	invalidOwnerURL         = "hermes_main_invalid_config: owner-url must be a valid WebSocket URL"
	invalidOwnerScheme      = "hermes_main_invalid_config: owner-url must use ws:// or wss://"
	invalidOwnerCredentials = "hermes_main_invalid_config: owner-url must omit credentials, query and fragment; use token-file for the raw gateway token"
	invalidOwnerPath        = "hermes_main_invalid_config: owner-url path must be exactly /api/ws"
	invalidOwnerPort        = "hermes_main_invalid_config: owner-url must include an explicit port from 1 to 65535"
	invalidOwnerHost        = "hermes_main_requires_loopback: owner-url host must be localhost or a loopback IP address"
	invalidMainSession      = "hermes_main_invalid_config: main-session-id must be a nonempty opaque ID without surrounding whitespace or control characters, at most 4096 bytes"
	invalidOwnerWorkdir     = "hermes_main_invalid_config: workdir must be an absolute clean path without trailing separators or dot components"
	invalidTokenPath        = "hermes_main_invalid_token_file: token-file must be an absolute path"
	invalidTokenContent     = "hermes_main_invalid_token_file: token-file must be a regular file containing a nonempty raw token, at most 8192 bytes"
	unavailableTokenFile    = "hermes_main_token_unavailable: token-file must name a readable local file"
	mainOnlyBridge          = "hermes_main_execution_adapter_required: configure this notification bridge in main_command; use a native Hermes or full execution adapter in command"
	invalidDesktopContract  = "hermes_main_unsupported_protocol: info.desktop_contract must be 8; the Hermes gateway must support hidden prompts, omitted history and persisted-turn receipts"
)

// DiagnosticMessage admits only fixed messages authored by this bridge. Never
// pass through host RPC messages, URLs, token bytes, file paths or stderr text.
func DiagnosticMessage(message string) string {
	switch message {
	case "workbuddy_host_pid_unreadable: cannot read the Sidecar discovery file; existing task and binding unchanged",
		"workbuddy_host_status_unknown: Sidecar discovery file is absent and Desktop process discovery failed; existing task and binding unchanged",
		"workbuddy_host_not_running: open and sign in to WorkBuddy, then retry; existing task and binding unchanged",
		"workbuddy_sidecar_not_ready: WorkBuddy Desktop is running but its host-owned Sidecar has not started; open a local conversation in WorkBuddy to initialize its runtime, then retry watch start --host workbuddy; existing task and binding unchanged":
		return message

	case invalidOwnerURL, invalidOwnerScheme, invalidOwnerCredentials, invalidOwnerPath,
		invalidOwnerPort, invalidOwnerHost, invalidMainSession, invalidOwnerWorkdir, invalidTokenPath, invalidTokenContent, unavailableTokenFile, mainOnlyBridge, invalidDesktopContract,
		"hermes_main_invalid_config", "hermes_main_requires_loopback", "hermes_main_invalid_token_file",
		"hermes_main_token_unavailable", "hermes_main_connection_failed", "hermes_main_connection_closed",
		"hermes_main_invalid_frame", "hermes_main_unsupported_protocol", "hermes_main_write_failed",
		"hermes_main_requires_user", "hermes_main_response_unconfirmed", "hermes_main_rpc_failed",
		"hermes_main_session_unavailable", "hermes_main_backend_changed", "hermes_main_busy",
		"hermes_main_invalid_client_request", "hermes_main_target_mismatch", "hermes_main_unsupported_method":
		return message
	}
	return ""
}
