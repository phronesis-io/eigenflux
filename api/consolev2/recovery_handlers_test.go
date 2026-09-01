package consolev2

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMaskedEigenFluxIDNeverExposesTheCompleteShortID(t *testing.T) {
	for input, want := range map[string]string{
		"AbCdE": "eigenflux#Ab***",
		" Z ":   "eigenflux#*****",
		"":      "eigenflux#*****",
	} {
		got := maskedEigenFluxID(input)
		if got != want {
			t.Fatalf("maskedEigenFluxID(%q) = %q, want %q", input, got, want)
		}
		if strings.Contains(got, strings.TrimSpace(input)) && len(strings.TrimSpace(input)) >= 3 {
			t.Fatalf("masked ID %q exposes input %q", got, input)
		}
	}
}

func TestAccountRecoveryCredentialLifetimeIsShort(t *testing.T) {
	if accountRecoveryTTL <= 0 || accountRecoveryTTL > 5*time.Minute {
		t.Fatalf("accountRecoveryTTL = %s, want no more than five minutes", accountRecoveryTTL)
	}
}

func TestRecoverySourceDispositionUsesEmailBindingAsLifecycleBoundary(t *testing.T) {
	if got := recoverySourceDisposition(0); got != recoverySourceAbandon {
		t.Fatalf("unbound source disposition = %q, want %q", got, recoverySourceAbandon)
	}
	if got := recoverySourceDisposition(42); got != recoverySourcePreserve {
		t.Fatalf("email-bound source disposition = %q, want %q", got, recoverySourcePreserve)
	}
}

func TestValidActiveIdentityBindingRejectsStaleOwnership(t *testing.T) {
	revokedAt := int64(123)
	for _, test := range []struct {
		name             string
		agentID          int64
		principalAgentID int64
		identityState    string
		principalStatus  string
		principalRevoked *int64
		want             bool
	}{
		{name: "active owner", agentID: 10, principalAgentID: 10, identityState: "active", principalStatus: "active", want: true},
		{name: "limited owner", agentID: 10, principalAgentID: 10, identityState: "active", principalStatus: "limited", want: true},
		{name: "principal moved", agentID: 10, principalAgentID: 20, identityState: "active", principalStatus: "active"},
		{name: "temporary abandoned", agentID: 10, principalAgentID: 10, identityState: "recovered_temporary", principalStatus: "active"},
		{name: "principal suspended", agentID: 10, principalAgentID: 10, identityState: "active", principalStatus: "suspended"},
		{name: "principal revoked", agentID: 10, principalAgentID: 10, identityState: "active", principalStatus: "active", principalRevoked: &revokedAt},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validActiveIdentityBinding(test.agentID, test.principalAgentID, test.identityState, test.principalStatus, test.principalRevoked); got != test.want {
				t.Fatalf("validActiveIdentityBinding() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestHistoricalIdentityCardBackfillsEveryCanonicalFieldWithoutEmptyOverwrite(t *testing.T) {
	profile := map[string]interface{}{
		"working_languages": []interface{}{},
		"seeking":           []interface{}{"canonical collaborators"},
		"geo":               "SG",
		"agent_status":      []interface{}{},
	}
	publicCard := map[string]interface{}{
		"agent_description": "Historical research agent",
		"human_description": "Supports a product builder",
		"working_languages": []interface{}{"中文", "English"},
		"seeking":           []interface{}{"stale seeking"},
		"offering":          []interface{}{"research", "synthesis"},
	}
	privateCard := map[string]interface{}{
		"geo":                "US",
		"timezone":           "Asia/Singapore",
		"current_focus":      []interface{}{"shipping recovery"},
		"demands":            []interface{}{"security review"},
		"agent_status":       []interface{}{"researching"},
		"human_status":       []interface{}{"building"},
		"interests_negative": []interface{}{"spam"},
	}
	got := historicalIdentityCard("Atlas", "", "CN", profile, publicCard, privateCard)
	want := map[string]interface{}{
		"agent_name":         "Atlas",
		"bio":                "Historical research agent",
		"agent_description":  "Historical research agent",
		"human_description":  "Supports a product builder",
		"working_languages":  []string{"中文", "English"},
		"seeking":            []string{"canonical collaborators"},
		"offering":           []string{"research", "synthesis"},
		"geo":                "SG",
		"timezone":           "Asia/Singapore",
		"current_focus":      []string{"shipping recovery"},
		"demands":            []string{"security review"},
		"agent_status":       []string{"researching"},
		"human_status":       []string{"building"},
		"interests_negative": []string{"spam"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("historical identity card = %#v, want %#v", got, want)
	}
}

func TestMergeHistoricalRecoveryDraftOnlyFillsMissingIdentityFields(t *testing.T) {
	current := map[string]interface{}{"identity_card": map[string]interface{}{
		"agent_name":        "Human edited name",
		"working_languages": []interface{}{},
		"seeking":           []interface{}{"existing need"},
	}}
	historical := map[string]interface{}{"identity_card": map[string]interface{}{
		"agent_name":        "Historical name",
		"working_languages": []interface{}{"中文", "English"},
		"seeking":           []interface{}{"historical need"},
		"offering":          []interface{}{"research"},
	}}
	got := mergeHistoricalRecoveryDraft(current, historical)["identity_card"].(map[string]interface{})
	if got["agent_name"] != "Human edited name" || !reflect.DeepEqual(got["seeking"], []interface{}{"existing need"}) {
		t.Fatalf("existing historical draft fields were overwritten: %#v", got)
	}
	if !reflect.DeepEqual(got["working_languages"], []interface{}{"中文", "English"}) ||
		!reflect.DeepEqual(got["offering"], []interface{}{"research"}) {
		t.Fatalf("missing historical fields were not backfilled: %#v", got)
	}
}

func TestHistoricalDraftLocationCompatibilityClearsUnknownOptionalValues(t *testing.T) {
	_, draft, err := normalizeHistoricalOnboardingDraftJSON([]byte(`{
		"identity_card": {
			"geo": "Atlantis",
			"timezone": "Berlin local time",
			"working_languages": ["English"]
		}
	}`))
	if err != nil {
		t.Fatalf("legacy optional location values blocked recovery: %v", err)
	}
	identity := draft["identity_card"].(map[string]interface{})
	if identity["geo"] != "" || identity["timezone"] != "" {
		t.Fatalf("unknown legacy location values were not cleared: %#v", identity)
	}
}

func TestHistoricalDraftLocationCompatibilityNormalizesRecognizedValues(t *testing.T) {
	_, draft, err := normalizeHistoricalOnboardingDraftJSON([]byte(`{
		"identity_card": {
			"geo": "Singapore",
			"timezone": "UTC+8"
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	identity := draft["identity_card"].(map[string]interface{})
	if identity["geo"] != "SG" || identity["timezone"] != "Asia/Singapore" {
		t.Fatalf("recognized legacy location values were not normalized: %#v", identity)
	}
}

func TestHistoricalDraftLocationCompatibilityConvertsLegacyCountryName(t *testing.T) {
	_, draft, err := normalizeHistoricalOnboardingDraftJSON([]byte(`{
		"identity_card": {
			"geo": "Germany",
			"timezone": "Europe/Berlin"
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	identity := draft["identity_card"].(map[string]interface{})
	if identity["geo"] != "DE" || identity["timezone"] != "Europe/Berlin" {
		t.Fatalf("legacy country display name was not converted: %#v", identity)
	}
}

func TestHistoricalDraftLocationCompatibilityKeepsValidTimezoneWhenCountryIsUnknown(t *testing.T) {
	_, draft, err := normalizeHistoricalOnboardingDraftJSON([]byte(`{
		"identity_card": {
			"geo": "Atlantis",
			"timezone": "Europe/Berlin"
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	identity := draft["identity_card"].(map[string]interface{})
	if identity["geo"] != "" || identity["timezone"] != "Europe/Berlin" {
		t.Fatalf("valid legacy timezone was not preserved independently: %#v", identity)
	}
}
