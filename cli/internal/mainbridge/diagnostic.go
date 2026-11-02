package mainbridge

const (
	invalidOwnerURL         = "hermes_main_invalid_config: owner-url must be a valid WebSocket URL"
	invalidOwnerScheme      = "hermes_main_invalid_config: owner-url must use ws:// or wss://"
	invalidOwnerCredentials = "hermes_main_invalid_config: owner-url must omit credentials, query and fragment; use token-file for the raw gateway token"
	invalidOwnerPath        = "hermes_main_invalid_config: owner-url path must be exactly /api/ws"
	invalidOwnerPort        = "hermes_main_invalid_config: owner-url must include an explicit port from 1 to 65535"
	invalidMainSession      = "hermes_main_invalid_config: main-session-id must be a nonempty opaque ID without surrounding whitespace or control characters, at most 4096 bytes"
	invalidOwnerWorkdir     = "hermes_main_invalid_config: workdir must be an absolute clean path without trailing separators or dot components"
	invalidTokenPath        = "hermes_main_invalid_token_file: token-file must be an absolute path"
	invalidTokenContent     = "hermes_main_invalid_token_file: token-file must be a regular file containing a nonempty raw token, at most 8192 bytes"
	mainOnlyBridge          = "hermes_main_execution_adapter_required: configure this notification bridge in main_command; use a native Hermes or full execution adapter in command"
)

// DiagnosticMessage admits only fixed messages authored by this bridge. Never
// pass through host RPC messages, URLs, token bytes, file paths or stderr text.
func DiagnosticMessage(message string) string {
	switch message {
	case invalidOwnerURL, invalidOwnerScheme, invalidOwnerCredentials, invalidOwnerPath,
		invalidOwnerPort, invalidMainSession, invalidOwnerWorkdir, invalidTokenPath, invalidTokenContent, mainOnlyBridge,
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
