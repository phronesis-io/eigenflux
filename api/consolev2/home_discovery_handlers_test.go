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
	got := selectUniqueHomeDiscovery(rules)
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
	got := selectUniqueHomeDiscovery(rules)
	if len(got) != 1 || got[0].Key != "recognized" {
		t.Fatalf("unexpected selection: %#v", got)
	}
}
