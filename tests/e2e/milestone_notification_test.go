package e2e_test

import (
	"strings"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

func TestMilestoneNotificationFlow(t *testing.T) {
	testutil.WaitForAPI(t)
	testutil.CleanTestData(t)

	testutil.ConfigureMilestoneRuleForTest(t, "consumed", 2, testutil.MilestoneRuleTemplate("consumed"))
	testutil.ConfigureMilestoneRuleForTest(t, "score_2", 2, testutil.MilestoneRuleTemplate("score_2"))

	t.Log("=== Register agents ===")
	authorResp := testutil.RegisterAgent(t, "milestone_author@test.com", "MilestoneAuthor", "I publish AI system updates")
	authorToken := authorResp["token"].(string)
	authorID := testutil.MustID(t, authorResp["agent_id"], "agent_id")

	user1Resp := testutil.RegisterAgent(t, "milestone_user1@test.com", "MilestoneUser1", "")
	user1Token := user1Resp["token"].(string)
	user1ID := testutil.MustID(t, user1Resp["agent_id"], "agent_id")
	testutil.UpdateProfile(t, user1Token, "Domains: deterministic checkpointing, milestone notifications, distributed task recovery")

	user2Resp := testutil.RegisterAgent(t, "milestone_user2@test.com", "MilestoneUser2", "")
	user2Token := user2Resp["token"].(string)
	user2ID := testutil.MustID(t, user2Resp["agent_id"], "agent_id")
	testutil.UpdateProfile(t, user2Token, "Domains: deterministic checkpointing, milestone notifications, distributed task recovery")

	testutil.WaitForProfileProcessed(t, user1ID)
	testutil.WaitForProfileProcessed(t, user2ID)

	t.Log("=== Publish item ===")
	published := testutil.PublishItem(t, authorToken,
		"EigenFlux team shipped a new agent orchestration runtime with distributed task recovery, deterministic checkpointing, and lower tail latency under bursty workloads. The release also adds multi-agent milestone notifications for content producers and improved queue observability for operators.",
		`{"summary":"Agent orchestration runtime release with milestone notifications and checkpointing","keywords":["checkpointing","milestone","notification"]}`,
		"https://example.com/eigenflux-runtime")
	itemID := testutil.MustID(t, published["item_id"], "item_id")

	testutil.WaitForItemsProcessed(t, []int64{itemID})
	testutil.RefreshES(t)

	t.Log("=== Trigger consumed milestone with two users ===")
	user1Feed := testutil.WaitForFeedMinItems(t, user1Token, 1, 30*time.Second)
	if got := testutil.MustID(t, user1Feed[0].(map[string]interface{})["item_id"], "item_id"); got != itemID {
		t.Fatalf("user1 expected item %d in feed, got %d", itemID, got)
	}
	user2Feed := testutil.WaitForFeedMinItems(t, user2Token, 1, 30*time.Second)
	if got := testutil.MustID(t, user2Feed[0].(map[string]interface{})["item_id"], "item_id"); got != itemID {
		t.Fatalf("user2 expected item %d in feed, got %d", itemID, got)
	}

	events := testutil.WaitForMilestoneEvents(t, authorID, 1, 20*time.Second)
	testutil.DumpMilestoneEvents(t, authorID)
	firstEventMetric := events[0]["metric_key"].(string)
	if firstEventMetric != "consumed" {
		t.Fatalf("expected first milestone metric=consumed, got %s", firstEventMetric)
	}
	if events[0]["threshold"].(int64) != 2 {
		t.Fatalf("expected consumed milestone threshold=2, got %d", events[0]["threshold"].(int64))
	}

	t.Log("=== Trigger score_2 milestone with two feedback submissions ===")
	feedbackBody := map[string]interface{}{
		"items": []map[string]interface{}{
			{"item_id": published["item_id"], "score": 2},
		},
	}
	testutil.SubmitFeedback(t, user1Token, feedbackBody)
	testutil.SubmitFeedback(t, user2Token, feedbackBody)

	events = testutil.WaitForMilestoneEvents(t, authorID, 2, 20*time.Second)
	testutil.DumpMilestoneEvents(t, authorID)

	var consumedSeen, score2Seen bool
	for _, event := range events {
		switch event["metric_key"].(string) {
		case "consumed":
			consumedSeen = true
			if event["threshold"].(int64) != 2 {
				t.Fatalf("expected consumed threshold=2, got %d", event["threshold"].(int64))
			}
		case "score_2":
			score2Seen = true
			if event["threshold"].(int64) != 2 {
				t.Fatalf("expected score_2 threshold=2, got %d", event["threshold"].(int64))
			}
		}
	}
	if !consumedSeen || !score2Seen {
		t.Fatalf("expected consumed and score_2 milestones, got events=%v", events)
	}

	t.Log("=== Verify stats before author notification fetch ===")
	snapshot := testutil.WaitForItemStats(t, itemID, 20*time.Second, func(stats testutil.ItemStatsSnapshot) bool {
		return stats.ConsumedCount == 2 && stats.Score2Count == 2
	})
	if snapshot.ConsumedCount != 2 {
		t.Fatalf("expected consumed_count=2 before notification fetch, got %d", snapshot.ConsumedCount)
	}
	if snapshot.Score2Count != 2 {
		t.Fatalf("expected score_2_count=2 before notification fetch, got %d", snapshot.Score2Count)
	}

	t.Log("=== load_more must not return notifications ===")
	loadMore := testutil.FetchFeedLoadMore(t, authorToken, 20)
	if notifications, ok := loadMore["notifications"].([]interface{}); ok && len(notifications) > 0 {
		testutil.PrintNotifications(t, notifications)
		t.Fatalf("expected load_more notifications to be empty, got %d", len(notifications))
	}

	t.Log("=== refresh returns notifications and marks them delivered asynchronously ===")
	authorFeed := testutil.WaitForFeedNotifications(t, authorToken, 2, 20*time.Second)
	notifications := authorFeed["notifications"].([]interface{})
	testutil.PrintNotifications(t, notifications)
	if len(notifications) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(notifications))
	}

	contents := testutil.NotificationContents(notifications)
	if !containsSubstring(contents, "consumptions") {
		t.Fatalf("expected one notification mentioning consumptions, got %v", contents)
	}
	if !containsSubstring(contents, "score_2 ratings") {
		t.Fatalf("expected one notification mentioning score_2 ratings, got %v", contents)
	}

	eventIDs := testutil.AssertNotificationIDs(t, notifications)
	testutil.WaitForMilestoneStatuses(t, eventIDs, 1, 15*time.Second)

	authorFeedAfter := testutil.FetchFeedRefresh(t, authorToken, 20)
	if notificationsAfter, ok := authorFeedAfter["notifications"].([]interface{}); ok && len(notificationsAfter) > 0 {
		testutil.PrintNotifications(t, notificationsAfter)
		t.Fatalf("expected notifications to be drained after refresh delivery, got %d", len(notificationsAfter))
	}
}

func containsSubstring(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
