package consolev2

import (
	"strings"
	"testing"

	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
)

func TestFeedIntentMatchAssessmentFallback(t *testing.T) {
	summary := "AI agent reliability"
	item := &feedrpc.FeedItem{ItemId: 42, Summary: &summary}
	revision := int64(7)
	for _, tc := range []struct {
		name    string
		intents []feedIntent
		status  string
		reason  string
		actions int
	}{
		{"null intents", nil, "unmatched", "assess relevance using network_goal", 0},
		{"empty intents", []feedIntent{}, "unmatched", "assess relevance using network_goal", 0},
		{"nonmatching intent", []feedIntent{{IntentID: "1", WatchFor: "gardening"}}, "unmatched", "agent relevance assessment is required", 0},
		{"matching intent", []feedIntent{{IntentID: "1", WatchFor: "reliability", ActionPolicy: "network_action", ActionInstruction: "Discuss reliability"}}, "matched", "matched confirmed intent terms", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match, actions := matchFeedIntents(10, &revision, item, tc.intents)
			if match["status"] != tc.status || !strings.Contains(match["reason"].(string), tc.reason) || len(actions) != tc.actions {
				t.Fatalf("match=%v actions=%v", match, actions)
			}
			if tc.actions == 0 && (match["score"] != float64(0) || len(match["matched_intent_ids"].([]string)) != 0) {
				t.Fatalf("fallback fabricated an intent match: %v", match)
			}
			if tc.actions == 1 && (match["score"] != float64(1) || actions[0].(map[string]interface{})["requires_user_confirmation"] != true) {
				t.Fatalf("confirmed intent policy changed: %v %v", match, actions)
			}
		})
	}
}
