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
			"geo": "CN",
			"timezone": "Asia/Shanghai"
		},
		"security_boundary": {"recurring_publish": false},
		"network_goal": "",
		"intent_actions": []
	}`)
	got := deriveInitialProvenance(draft, provenanceAgent, map[string]string{
		"identity_card.geo": fieldSourceAgentUserContext,
	}, 123)
	if len(got) != 4 {
		t.Fatalf("provenance count = %d, want 4: %#v", len(got), got)
	}
	if got["identity_card.geo"].ValueSource != fieldSourceAgentUserContext ||
		got["identity_card.agent_name"].ValueSource != fieldSourceAgentInferred {
		t.Fatalf("Agent source classification was not preserved: %#v", got)
	}
	if got["identity_card.agent_description"].ValueSource != "" {
		t.Fatalf("empty field must not have provenance: %#v", got)
	}
}

func TestMergeOnboardingDraftHumanOwnershipBlocksLaterAgent(t *testing.T) {
	original := mustDraftObject(t, `{
		"identity_card":{"agent_name":"Atlas","geo":"CN"},
		"network_goal":"Find AI infrastructure signals",
		"intent_actions":[]
	}`)
	humanEdit := mustDraftObject(t, `{
		"identity_card":{"agent_name":"Atlas Research","geo":"CN"},
		"network_goal":"Find AI infrastructure signals",
		"intent_actions":[]
	}`)
	initial := deriveInitialProvenance(original, provenanceAgent, nil, 1)
	merged, afterHuman, blocked := mergeOnboardingDraft(original, humanEdit, initial, provenanceHuman, nil, 2)
	if len(blocked) != 0 {
		t.Fatalf("human edit unexpectedly blocked: %v", blocked)
	}
	nameSource := afterHuman["identity_card.agent_name"]
	if nameSource.OriginSource != fieldSourceAgentInferred || nameSource.ValueSource != fieldSourceHumanInput || !nameSource.HumanConfirmed {
		t.Fatalf("human ownership not recorded: %#v", afterHuman)
	}

	agentRetry := mustDraftObject(t, `{
		"identity_card":{"agent_name":"Agent overwrite","geo":"SG"},
		"network_goal":"Find AI infrastructure signals",
		"intent_actions":[]
	}`)
	merged, afterAgent, blocked := mergeOnboardingDraft(merged, agentRetry, afterHuman, provenanceAgent, map[string]string{
		"identity_card.geo": fieldSourceAgentUserContext,
	}, 3)
	name, _ := draftPathValue(merged, "identity_card.agent_name")
	if name != "Atlas Research" {
		t.Fatalf("agent overwrote human value: %v", name)
	}
	geo, _ := draftPathValue(merged, "identity_card.geo")
	if geo != "SG" {
		t.Fatalf("agent-owned field did not refresh: %v", geo)
	}
	if !reflect.DeepEqual(blocked, []string{"identity_card.agent_name"}) {
		t.Fatalf("blocked paths mismatch: %v", blocked)
	}
	if afterAgent["identity_card.agent_name"].ValueSource != fieldSourceHumanInput ||
		afterAgent["identity_card.geo"].ValueSource != fieldSourceAgentUserContext {
		t.Fatalf("sources mismatch after Agent retry: %#v", afterAgent)
	}
}

func TestConfirmStepProvenancePreservesOriginAndLocksAgentRetries(t *testing.T) {
	draft := mustDraftObject(t, `{"identity_card":{"agent_name":"Atlas","geo":"CN"}}`)
	provenance := deriveInitialProvenance(draft, provenanceAgent, map[string]string{
		"identity_card.geo": fieldSourceAgentUserContext,
	}, 1)
	confirmed := confirmStepProvenance(draft, provenance, 2, 2)
	entry := confirmed["identity_card.geo"]
	if entry.OriginSource != fieldSourceAgentUserContext || entry.ValueSource != fieldSourceAgentUserContext || !entry.HumanConfirmed {
		t.Fatalf("confirmation did not preserve origin: %#v", entry)
	}

	retry := mustDraftObject(t, `{"identity_card":{"agent_name":"Atlas","geo":"SG"}}`)
	merged, _, blocked := mergeOnboardingDraft(draft, retry, confirmed, provenanceAgent, nil, 3)
	geo, _ := draftPathValue(merged, "identity_card.geo")
	if geo != "CN" || !reflect.DeepEqual(blocked, []string{"identity_card.agent_name", "identity_card.geo"}) {
		t.Fatalf("confirmed fields were not protected: geo=%v blocked=%v", geo, blocked)
	}
}

func TestCanonicalSourceUsesStoredOwnership(t *testing.T) {
	provenance := map[string]fieldProvenance{
		"network_goal":   {OriginSource: fieldSourceAgentInferred, ValueSource: fieldSourceAgentInferred, LastActorType: fieldActorAgent},
		"intent_actions": {OriginSource: fieldSourceSystemGenerated, ValueSource: fieldSourceSystemGenerated, LastActorType: fieldActorSystem},
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
