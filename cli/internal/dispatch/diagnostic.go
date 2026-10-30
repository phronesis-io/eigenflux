package dispatch

import "encoding/json"

// DiagnosticError retains fixed diagnostic categories without storing tool input,
// process output, paths or the model's permission explanation.
type DiagnosticError struct {
	Cause     error
	Code      string
	Operation string
}

func (e *DiagnosticError) Error() string { return e.Code }
func (e *DiagnosticError) Unwrap() error { return e.Cause }

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
