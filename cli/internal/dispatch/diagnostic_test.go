package dispatch

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPermissionDiagnosticRetainsOnlyOperationAndNeedsUser(t *testing.T) {
	for _, kind := range []string{"read", "edit", "execute", "fetch", "unknown-secret"} {
		payload, _ := json.Marshal(map[string]any{"toolCall": map[string]string{"kind": kind, "title": "/Users/private/credentials", "rawInput": "secret"}})
		err := permissionDiagnostic(payload)
		var diagnostic *DiagnosticError
		if !errors.Is(err, ErrNeedsUser) || !errors.As(err, &diagnostic) {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(diagnostic)
		if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "/Users/") {
			t.Fatal(string(raw))
		}
		if kind == "read" && diagnostic.Operation != "read" {
			t.Fatal(diagnostic)
		}
	}
}
