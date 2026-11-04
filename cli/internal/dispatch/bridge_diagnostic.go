package dispatch

import (
	"strings"
	"sync"

	"cli.eigenflux.ai/internal/mainbridge"
)

type bridgeDiagnosticError struct {
	cause   error
	message string
}

func (e *bridgeDiagnosticError) Error() string { return e.cause.Error() + ": " + e.message }
func (e *bridgeDiagnosticError) Unwrap() error { return e.cause }

// Keep at most one bounded in-flight line and a fixed, allowlisted diagnostic.
// Arbitrary process output is never attached to errors or persisted.
type bridgeDiagnosticWriter struct {
	mu         sync.Mutex
	line       []byte
	overflow   bool
	diagnostic string
}

func (w *bridgeDiagnosticWriter) consumeLine() {
	if !w.overflow {
		line := strings.TrimSpace(string(w.line))
		line = strings.TrimPrefix(line, "Error: ")
		if message := mainbridge.DiagnosticMessage(line); message != "" {
			w.diagnostic = message
		} else if safeBridgeCode(line) != "unknown_failure" {
			w.diagnostic = line
		}
	}
	w.line, w.overflow = w.line[:0], false
}

func (w *bridgeDiagnosticWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			w.consumeLine()
		} else if len(w.line) < 512 && !w.overflow {
			w.line = append(w.line, b)
		} else {
			w.overflow = true
		}
	}
	return len(p), nil
}

func (w *bridgeDiagnosticWriter) message() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.consumeLine()
	return w.diagnostic
}
