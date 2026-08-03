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
