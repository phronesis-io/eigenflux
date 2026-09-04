package consolev2

import (
	"testing"
	"time"
)

func TestHomeDiscoveryDayStartUsesAgentTimezone(t *testing.T) {
	now := time.Date(2026, 9, 3, 7, 45, 0, 0, time.FixedZone("SGT", 8*60*60))
	location, err := time.LoadLocation("Asia/Singapore")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 3, 0, 0, 0, 0, location).UTC().UnixMilli()
	if got := homeDiscoveryDayStart(now, location); got != want {
		t.Fatalf("day start = %d, want %d", got, want)
	}
}

func TestSelectUniqueHomeDiscoveryUsesNextCandidate(t *testing.T) {
	rules := []homeDiscoveryRule{
		{Key: "recognized", Rows: []homeDiscoveryCandidateRow{{AgentID: 11}, {AgentID: 12}}},
		{Key: "active", Rows: []homeDiscoveryCandidateRow{{AgentID: 11}, {AgentID: 13}}},
		{Key: "relations", Rows: []homeDiscoveryCandidateRow{{AgentID: 13}, {AgentID: 14}}},
	}
	got := selectUniqueHomeDiscovery(rules, 3)
	if len(got) != 3 {
		t.Fatalf("selected %d rules, want 3", len(got))
	}
	want := []int64{11, 13, 14}
	for i := range want {
		if got[i].Rows[0].AgentID != want[i] {
			t.Fatalf("selected[%d] = %d, want %d", i, got[i].Rows[0].AgentID, want[i])
		}
	}
}

func TestSelectUniqueHomeDiscoveryOmitsRuleWithoutUniqueCandidate(t *testing.T) {
	rules := []homeDiscoveryRule{
		{Key: "recognized", Rows: []homeDiscoveryCandidateRow{{AgentID: 11}}},
		{Key: "active", Rows: []homeDiscoveryCandidateRow{{AgentID: 11}}},
	}
	got := selectUniqueHomeDiscovery(rules, 2)
	if len(got) != 1 || got[0].Key != "recognized" {
		t.Fatalf("unexpected selection: %#v", got)
	}
}

func TestSelectUniqueHomeDiscoverySkipsEigenfluxOfficialAssistant(t *testing.T) {
	rules := []homeDiscoveryRule{
		{Key: "relations", Rows: []homeDiscoveryCandidateRow{
			{AgentID: eigenfluxOfficialAssistantID},
			{AgentID: 42},
		}},
	}

	got := selectUniqueHomeDiscovery(rules, 1)
	if len(got) != 1 || got[0].Rows[0].AgentID != 42 {
		t.Fatalf("selection = %#v, want next eligible agent 42", got)
	}
}

func TestSelectUniqueHomeDiscoveryReturnsMultipleAgentsPerRuleInRounds(t *testing.T) {
	rules := []homeDiscoveryRule{
		{Key: "recognized", Rows: []homeDiscoveryCandidateRow{{AgentID: 11}, {AgentID: 12}, {AgentID: 13}}},
		{Key: "active", Rows: []homeDiscoveryCandidateRow{{AgentID: 21}, {AgentID: 22}, {AgentID: 23}}},
	}

	got := selectUniqueHomeDiscovery(rules, 5)
	if len(got) != 5 {
		t.Fatalf("selected %d agents, want 5", len(got))
	}
	wantIDs := []int64{11, 21, 12, 22, 13}
	wantRules := []string{"recognized", "active", "recognized", "active", "recognized"}
	for index := range wantIDs {
		if got[index].Rows[0].AgentID != wantIDs[index] || got[index].Key != wantRules[index] {
			t.Fatalf("selected[%d] = %s/%d, want %s/%d", index, got[index].Key, got[index].Rows[0].AgentID, wantRules[index], wantIDs[index])
		}
	}
}

func TestHomeDiscoveryCapabilitiesUsePublicCardCapabilitiesAndOmitEmptyLabels(t *testing.T) {
	card := map[string]interface{}{
		"capabilities": []interface{}{"尚未设置", " Generate video ", "Not set", "Research"},
		"offering":     []interface{}{"This is public intent, not a capability"},
	}

	got := cardStrings(card, "capabilities", 3)
	want := []string{"Generate video", "Research"}
	if len(got) != len(want) {
		t.Fatalf("capabilities = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("capabilities[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestHomeDiscoveryRuntimeUsesPublicCardRuntime(t *testing.T) {
	card := map[string]interface{}{"runtime": " OpenClaw/1.2.3 "}
	if got := cardString(card, "runtime"); got != "OpenClaw/1.2.3" {
		t.Fatalf("runtime = %q, want %q", got, "OpenClaw/1.2.3")
	}
}
