package consolev2

import (
	"encoding/json"
	"testing"
)

func TestNormalizeOnboardingDraftLocationsUsesStableValues(t *testing.T) {
	tests := []struct {
		name         string
		geo          string
		timezone     string
		wantGeo      string
		wantTimezone string
	}{
		{name: "canonical", geo: "CN", timezone: "Asia/Shanghai", wantGeo: "CN", wantTimezone: "Asia/Shanghai"},
		{name: "display aliases", geo: "China", timezone: "Asia/Shanghai (UTC+8)", wantGeo: "CN", wantTimezone: "Asia/Shanghai"},
		{name: "Singapore offset", geo: "Singapore", timezone: "UTC+8", wantGeo: "SG", wantTimezone: "Asia/Singapore"},
		{name: "other stable values", geo: "FR", timezone: "Europe/Paris", wantGeo: "FR", wantTimezone: "Europe/Paris"},
		{name: "ambiguous offset stays empty", geo: "", timezone: "UTC+8", wantGeo: "", wantTimezone: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			draft := map[string]interface{}{"identity_card": map[string]interface{}{"geo": test.geo, "timezone": test.timezone}}
			if err := normalizeOnboardingDraftLocations(draft); err != nil {
				t.Fatal(err)
			}
			identity := draft["identity_card"].(map[string]interface{})
			if identity["geo"] != test.wantGeo || identity["timezone"] != test.wantTimezone {
				t.Fatalf("normalized location = %v/%v, want %s/%s", identity["geo"], identity["timezone"], test.wantGeo, test.wantTimezone)
			}
		})
	}
}

func TestNormalizeOnboardingDraftLocationsRejectsUnsupportedValues(t *testing.T) {
	for _, draft := range []map[string]interface{}{
		{"identity_card": map[string]interface{}{"geo": "Moon"}},
		{"identity_card": map[string]interface{}{"geo": "CN", "timezone": "UTC+25"}},
		{"identity_card": map[string]interface{}{"geo": 86}},
	} {
		if err := normalizeOnboardingDraftLocations(draft); err == nil {
			t.Fatalf("expected invalid location to fail: %#v", draft)
		}
	}
}

func TestNormalizeOnboardingDraftListsSupportsLegacyScalars(t *testing.T) {
	raw := json.RawMessage(`{
		"identity_card": {
			"working_languages": "中文 · English",
			"seeking": "",
			"offering": ["研究", " ", "任务整理"]
		}
	}`)
	normalized, _, err := normalizeOnboardingDraftJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	var payload draftPayload
	if err := json.Unmarshal(normalized, &payload); err != nil {
		t.Fatal(err)
	}
	if got, want := payload.IdentityCard.WorkingLanguages, []string{"中文", "English"}; !equalStrings(got, want) {
		t.Fatalf("working_languages = %#v, want %#v", got, want)
	}
	if len(payload.IdentityCard.Seeking) != 0 {
		t.Fatalf("seeking = %#v, want empty", payload.IdentityCard.Seeking)
	}
	if got, want := payload.IdentityCard.Offering, []string{"研究", "任务整理"}; !equalStrings(got, want) {
		t.Fatalf("offering = %#v, want %#v", got, want)
	}
}

func TestNormalizeOnboardingDraftListsRejectsInvalidTypes(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"identity_card":{"seeking":42}}`),
		json.RawMessage(`{"identity_card":{"seeking":["valid",42]}}`),
	} {
		if _, _, err := normalizeOnboardingDraftJSON(raw); err == nil {
			t.Fatalf("expected invalid list field to fail: %s", raw)
		}
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestNormalizeLoadedOnboardingDraftFillsPreReleaseEmptyProvenance(t *testing.T) {
	draft, err := normalizeLoadedOnboardingDraft(onboardingDraft{
		Revision: 2,
		DraftData: json.RawMessage(`{
			"identity_card":{"agent_name":"Atlas","geo":"China","timezone":"Asia/Shanghai (UTC+8)"}
		}`),
		FieldProvenance: json.RawMessage(`{}`),
		ActorType:       provenanceAgent,
		CreatedAt:       123,
	})
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(draft.DraftData, &data); err != nil {
		t.Fatal(err)
	}
	identity := data["identity_card"].(map[string]interface{})
	if identity["geo"] != "CN" || identity["timezone"] != "Asia/Shanghai" {
		t.Fatalf("loaded locations were not normalized: %#v", identity)
	}
	provenance := decodeProvenance(draft.FieldProvenance)
	entry := provenance["identity_card.geo"]
	if entry.OriginSource != fieldSourceAgentInferred || entry.ValueSource != fieldSourceAgentInferred || entry.UpdatedAt != 123 {
		t.Fatalf("empty pre-release provenance was not derived: %#v", entry)
	}
}
