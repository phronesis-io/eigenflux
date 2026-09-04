package consolev2

import "testing"

func TestSelectUniqueHomeWorthWatchingKeepsRuleOrderAndPrefersAgentDiversity(t *testing.T) {
	rules := []homeWorthWatchingRule{
		{Key: "trending", Rows: []homeWorthWatchingCandidate{{ItemID: 101, AgentID: 1}, {ItemID: 102, AgentID: 2}}},
		{Key: "participating", Rows: []homeWorthWatchingCandidate{{ItemID: 103, AgentID: 1}, {ItemID: 104, AgentID: 3}}},
		{Key: "helpful", Rows: []homeWorthWatchingCandidate{{ItemID: 104, AgentID: 3}, {ItemID: 105, AgentID: 4}}},
	}
	got := selectUniqueHomeWorthWatching(rules, 3)
	if len(got) != 3 {
		t.Fatalf("selected %d rules, want 3", len(got))
	}
	wantKeys := []string{"trending", "participating", "helpful"}
	wantItems := []int64{101, 104, 105}
	for i := range wantKeys {
		if got[i].Key != wantKeys[i] || got[i].Rows[0].ItemID != wantItems[i] {
			t.Fatalf("selected[%d] = %#v, want key=%s item=%d", i, got[i], wantKeys[i], wantItems[i])
		}
	}
}

func TestSelectUniqueHomeWorthWatchingAllowsRepeatedAgentOnlyToFillRule(t *testing.T) {
	rules := []homeWorthWatchingRule{
		{Key: "trending", Rows: []homeWorthWatchingCandidate{{ItemID: 101, AgentID: 1}}},
		{Key: "helpful", Rows: []homeWorthWatchingCandidate{{ItemID: 102, AgentID: 1}}},
	}
	got := selectUniqueHomeWorthWatching(rules, 2)
	if len(got) != 2 || got[0].Rows[0].ItemID != 101 || got[1].Rows[0].ItemID != 102 {
		t.Fatalf("unexpected fallback selection: %#v", got)
	}
}

func TestHomeWorthWatchingCacheKeyIncludesPolicyAndDay(t *testing.T) {
	got := homeWorthWatchingCacheKey("Asia/Singapore", 12345)
	want := "console:v2:home:worth-watching:weekly-v3:homepage-v2:Asia_Singapore:12345"
	if got != want {
		t.Fatalf("cache key = %q, want %q", got, want)
	}
}

func TestSelectPreferredHomeWorthWatchingPutsAllRealWorldSignalsFirst(t *testing.T) {
	preferred := homeWorthWatchingRule{Key: "real_world_signal", Rows: []homeWorthWatchingCandidate{
		{ItemID: 101, AgentID: 1}, {ItemID: 102, AgentID: 2},
	}}
	rules := []homeWorthWatchingRule{
		{Key: "trending", Rows: []homeWorthWatchingCandidate{{ItemID: 101, AgentID: 1}, {ItemID: 201, AgentID: 3}}},
		{Key: "helpful", Rows: []homeWorthWatchingCandidate{{ItemID: 202, AgentID: 4}}},
	}
	got := selectPreferredHomeWorthWatching(preferred, rules, 4)
	wantItems := []int64{101, 102, 201, 202}
	wantKeys := []string{"real_world_signal", "real_world_signal", "trending", "helpful"}
	for i := range wantItems {
		if got[i].Rows[0].ItemID != wantItems[i] || got[i].Key != wantKeys[i] {
			t.Fatalf("selected[%d] = %#v, want key=%s item=%d", i, got[i], wantKeys[i], wantItems[i])
		}
	}
}

func TestSelectUniqueHomeWorthWatchingFillsRoundRobinToLimit(t *testing.T) {
	rules := []homeWorthWatchingRule{
		{Key: "trending", Rows: []homeWorthWatchingCandidate{{ItemID: 101, AgentID: 1}, {ItemID: 102, AgentID: 2}, {ItemID: 103, AgentID: 3}}},
		{Key: "helpful", Rows: []homeWorthWatchingCandidate{{ItemID: 201, AgentID: 4}, {ItemID: 202, AgentID: 5}, {ItemID: 203, AgentID: 6}}},
	}
	got := selectUniqueHomeWorthWatching(rules, 4)
	wantItems := []int64{101, 201, 102, 202}
	for i, want := range wantItems {
		if got[i].Rows[0].ItemID != want {
			t.Fatalf("selected[%d] item = %d, want %d", i, got[i].Rows[0].ItemID, want)
		}
	}
}
