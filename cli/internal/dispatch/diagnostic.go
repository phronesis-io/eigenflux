package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"cli.eigenflux.ai/internal/mainbridge"
	"cli.eigenflux.ai/internal/maintenance"
)

// DiagnosticError retains fixed diagnostic categories without storing tool input,
// process output, paths or the model's permission explanation.
type DiagnosticError struct {
	Cause     error
	Code      string
	Operation string
	ExitCode  *int
	RPCCode   *int
}

type processDiagnosticError struct {
	cause    error
	exitCode int
}

func (e *processDiagnosticError) Error() string { return e.cause.Error() }
func (e *processDiagnosticError) Unwrap() error { return e.cause }

func (e *DiagnosticError) Error() string { return e.Code }
func (e *DiagnosticError) Unwrap() error { return e.Cause }

// SafeDiagnostic retains only fixed classifications and bounded numeric receipts.
// Error text, process output, host JSON-RPC messages and arguments never cross it.
func SafeDiagnostic(err error) *maintenance.CommissionBridgeError {
	if err == nil {
		return nil
	}
	b := &maintenance.CommissionBridgeError{Code: "unknown_failure"}
	var d *DiagnosticError
	if errors.As(err, &d) {
		b.Code = maintenance.CommissionBridgeCode(d.Code)
		if b.Code != "unknown_failure" {
			if d.Operation == "read" || d.Operation == "write" || d.Operation == "execute" || d.Operation == "network" {
				b.Operation = d.Operation
			}
			if d.ExitCode != nil && *d.ExitCode >= -1 && *d.ExitCode <= 255 {
				v := *d.ExitCode
				b.ExitCode = &v
			}
			if d.RPCCode != nil {
				v := *d.RPCCode
				test := &maintenance.CommissionBridgeError{Code: b.Code, RPCCode: &v}
				if maintenance.ValidateCommissionBridgeError(test) == nil {
					b.RPCCode = &v
				}
			}
		}
	}
	var bridge *bridgeDiagnosticError
	if errors.As(err, &bridge) {
		if code := safeBridgeCode(bridge.message); code != "unknown_failure" {
			b.Code = code
		}
	}
	if b.Code == "unknown_failure" {
		b.Code = fixedErrorCode(err, 0)
	}
	if b.Code == "unknown_failure" {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			b.Code = "timeout"
		case errors.Is(err, context.Canceled):
			b.Code = "cancelled"
		case errors.Is(err, ErrMainNoticeUnsupported):
			b.Code = "main_notice_transport_unverified"
		case errors.Is(err, ErrMainSessionMismatch):
			b.Code = "main_session_mismatch"
		case errors.Is(err, ErrMainSessionUnavailable):
			b.Code = "main_session_unavailable"
		case errors.Is(err, ErrMainNoticeBusy):
			b.Code = "main_notice_target_busy"
		case errors.Is(err, ErrNeedsUser):
			b.Code = "permission_required"
		default:
			b.Code = fixedErrorCode(err, 0)
		}
	}
	var process *processDiagnosticError
	if errors.As(err, &process) && process.exitCode >= -1 && process.exitCode <= 255 {
		if b.Code == "unknown_failure" && process.exitCode != 0 {
			b.Code = "runner_exit_failed"
		}
		if b.Code != "unknown_failure" {
			exit := process.exitCode
			b.ExitCode = &exit
		}
	}
	if b.Code != "unknown_failure" {
		b.Message = b.Code
	}
	return b
}

func safeBridgeCode(message string) string {
	if mainbridge.DiagnosticMessage(message) != "" {
		code, _, _ := strings.Cut(message, ":")
		return maintenance.CommissionBridgeCode(code)
	}
	// The complete value must be fixed; a valid-looking prefix cannot hide data.
	return maintenance.CommissionBridgeCode(message)
}

func permissionDiagnostic(raw json.RawMessage) error {
	var params struct {
		ToolCall struct {
			Kind string `json:"kind"`
		} `json:"toolCall"`
	}
	_ = json.Unmarshal(raw, &params)
	operation := ""
	switch params.ToolCall.Kind {
	case "read", "search":
		operation = "read"
	case "edit", "delete", "move":
		operation = "write"
	case "execute":
		operation = "execute"
	case "fetch":
		operation = "network"
	}
	return &DiagnosticError{Cause: ErrNeedsUser, Code: "permission_required", Operation: operation}
}

func fixedErrorCode(err error, depth int) string {
	if err == nil || depth >= 8 {
		return "unknown_failure"
	}
	if many, ok := err.(interface{ Unwrap() []error }); ok {
		parts := many.Unwrap()
		for i := len(parts) - 1; i >= 0; i-- {
			if code := fixedErrorCode(parts[i], depth+1); code != "unknown_failure" {
				return code
			}
		}
	}
	if next := errors.Unwrap(err); next != nil {
		if code := fixedErrorCode(next, depth+1); code != "unknown_failure" {
			return code
		}
	}
	return safeBridgeCode(err.Error())
}
