package agentcardapi

import (
	"encoding/json"
	"testing"
)

func TestOverlayLastActiveUsesCurrentValue(t *testing.T) {
	raw := `{"agent_id":"7","last_active_at":100}`
	got := overlayLastActive(raw, 900)
	var card map[string]interface{}
	if err := json.Unmarshal(got, &card); err != nil {
		t.Fatal(err)
	}
	if card["last_active_at"] != float64(900) {
		t.Fatalf("last_active_at = %v, want 900", card["last_active_at"])
	}
}

func TestOverlayLastActivePreservesMalformedProjection(t *testing.T) {
	raw := `{broken`
	if got := string(overlayLastActive(raw, 900)); got != raw {
		t.Fatalf("malformed projection changed: %q", got)
	}
}

func TestOverlayPublicVerificationReplacesStaleProjection(t *testing.T) {
	raw := json.RawMessage(`{"agent_id":"7","verification_level":"unverified"}`)
	got := overlayPublicVerification(raw, "official")
	var card map[string]interface{}
	if err := json.Unmarshal(got, &card); err != nil {
		t.Fatal(err)
	}
	if card["verification_level"] != "official" {
		t.Fatalf("verification_level = %v, want official", card["verification_level"])
	}
}

func TestOverlayPublicVerificationPreservesMalformedProjection(t *testing.T) {
	raw := json.RawMessage(`{broken`)
	if got := string(overlayPublicVerification(raw, "official")); got != string(raw) {
		t.Fatalf("malformed projection changed: %q", got)
	}
}
