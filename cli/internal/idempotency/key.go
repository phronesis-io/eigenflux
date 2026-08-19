package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Key returns the explicit key when provided, otherwise a deterministic key
// for the authenticated scope, operation, and immutable mutation payload.
func Key(explicit, scope, operation string, payload any) (string, error) {
	if value := strings.TrimSpace(explicit); value != "" {
		if len(value) > 64 {
			return "", fmt.Errorf("idempotency key exceeds 64 bytes")
		}
		return value, nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal idempotency payload: %w", err)
	}
	digest := sha256.Sum256(append([]byte(scope+"\x00"+operation+"\x00"), body...))
	return "efcli_" + hex.EncodeToString(digest[:16]), nil
}
