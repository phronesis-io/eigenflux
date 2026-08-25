package agentcardapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentCardPageSnapshotAvoidsProfileHistory(t *testing.T) {
	if strings.Contains(agentCardPageSnapshotSQL, "agent_profile_change_events") {
		t.Fatal("Agent Card page snapshot must not scan profile field history")
	}
	for _, source := range []string{"agent_network_goals", "agent_intent_actions", "agent_profiles"} {
		if !strings.Contains(agentCardPageSnapshotSQL, source) {
			t.Fatalf("Agent Card page snapshot is missing %s", source)
		}
	}
}

func TestCurrentAgentCardValuesUseCurrentFacts(t *testing.T) {
	values := currentAgentCardValues("Atlas", "Research assistant", map[string]json.RawMessage{
		"working_languages": json.RawMessage(`["中文","English"]`),
		"seeking":           json.RawMessage(`["AI infrastructure"]`),
	})
	if values["agent_name"] != "Atlas" || values["agent_description"] != "Research assistant" {
		t.Fatalf("identity facts were not projected: %#v", values)
	}
	languages, ok := values["working_languages"].([]interface{})
	if !ok || len(languages) != 2 {
		t.Fatalf("profile data was not decoded: %#v", values["working_languages"])
	}
}
