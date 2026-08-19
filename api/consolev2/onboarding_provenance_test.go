package consolev2

import (
	"encoding/json"
	"reflect"
	"testing"
)

func mustDraftObject(t *testing.T, value string) map[string]interface{} {
	t.Helper()
	result, err := decodeJSONObject(json.RawMessage(value))
	if err != nil {
		t.Fatalf("decode draft: %v", err)
	}
	return result
}

func TestDeriveInitialProvenanceOnlyLabelsActualValues(t *testing.T) {
	draft := mustDraftObject(t, `{
		"identity_card": {
			"agent_name": "Atlas",
			"agent_description": "",
			"working_languages": [],
			"geo": "China",
			"timezone": "Asia/Shanghai"
		},
		"security_boundary": {"recurring_publish": false},
		"network_goal": "",
		"intent_actions": []
	}`)
	got := deriveInitialProvenance(draft, provenanceAgent)
	want := map[string]string{
		"identity_card.agent_name":            provenanceAgent,
		"identity_card.geo":                   provenanceAgent,
		"identity_card.timezone":              provenanceAgent,
		"security_boundary.recurring_publish": provenanceAgent,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provenance mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestMergeOnboardingDraftHumanOwnershipBlocksLaterAgent(t *testing.T) {
	original := mustDraftObject(t, `{
		"identity_card":{"agent_name":"Atlas","geo":"China"},
		"network_goal":"Find AI infrastructure signals",
		"intent_actions":[]
	}`)
	humanEdit := mustDraftObject(t, `{
		"identity_card":{"agent_name":"Atlas Research","geo":"China"},
		"network_goal":"Find AI infrastructure signals",
		"intent_actions":[]
	}`)
	initial := deriveInitialProvenance(original, provenanceAgent)
	merged, afterHuman, blocked := mergeOnboardingDraft(original, humanEdit, initial, provenanceHuman)
	if len(blocked) != 0 {
		t.Fatalf("human edit unexpectedly blocked: %v", blocked)
	}
	if afterHuman["identity_card.agent_name"] != provenanceHuman {
		t.Fatalf("human ownership not recorded: %#v", afterHuman)
	}

	agentRetry := mustDraftObject(t, `{
		"identity_card":{"agent_name":"Agent overwrite","geo":"Singapore"},
		"network_goal":"Find AI infrastructure signals",
		"intent_actions":[]
	}`)
	merged, afterAgent, blocked := mergeOnboardingDraft(merged, agentRetry, afterHuman, provenanceAgent)
	name, _ := draftPathValue(merged, "identity_card.agent_name")
	if name != "Atlas Research" {
		t.Fatalf("agent overwrote human value: %v", name)
	}
	geo, _ := draftPathValue(merged, "identity_card.geo")
	if geo != "Singapore" {
		t.Fatalf("agent-owned field did not refresh: %v", geo)
	}
	if !reflect.DeepEqual(blocked, []string{"identity_card.agent_name"}) {
		t.Fatalf("blocked paths mismatch: %v", blocked)
	}
	if afterAgent["identity_card.agent_name"] != provenanceHuman || afterAgent["identity_card.geo"] != provenanceAgent {
		t.Fatalf("sources mismatch after Agent retry: %#v", afterAgent)
	}
}

func TestCanonicalSourceUsesStoredOwnership(t *testing.T) {
	provenance := map[string]string{
		"network_goal":   provenanceAgent,
		"intent_actions": provenanceSystem,
	}
	if got := canonicalSource(provenance, "network_goal"); got != provenanceAgent {
		t.Fatalf("network goal source = %q", got)
	}
	if got := canonicalSource(provenance, "intent_actions"); got != provenanceSystem {
		t.Fatalf("intent source = %q", got)
	}
	if got := canonicalSource(provenance, "missing"); got != provenanceHuman {
		t.Fatalf("legacy fallback source = %q", got)
	}
}
