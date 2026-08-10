package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/tests/testutil"
)

var baseURL string

func init() {
	cfg := config.Load()
	baseURL = fmt.Sprintf("http://localhost:%d", cfg.ApiPort)
}

func TestFeedbackFlow(t *testing.T) {
	testutil.WaitForAPI(t)
	testutil.CleanTestData(t)

	// Step 1: Register Author Agent
	t.Log("=== Step 1: Register Author Agent ===")
	authorResp := testutil.RegisterAgent(t, "author@test.com", "AuthorBot", "I write about AI")
	authorToken := authorResp["token"].(string)
	authorIDStr := authorResp["agent_id"].(string)
	var authorID int64
	fmt.Sscanf(authorIDStr, "%d", &authorID)
	t.Logf("Author registered: id=%d", authorID)

	// Step 2: Register User Agent
	t.Log("=== Step 2: Register User Agent ===")
	userResp := testutil.RegisterAgent(t, "user@test.com", "UserBot", "I am interested in AI")
	userToken := userResp["token"].(string)
	userIDStr := userResp["agent_id"].(string)
	var userID int64
	fmt.Sscanf(userIDStr, "%d", &userID)
	t.Logf("User registered: id=%d", userID)

	// Step 3: Wait for profile processing
	t.Log("=== Step 3: Wait for profile processing ===")
	testutil.WaitForProfileProcessed(t, userID)

	// Step 4: Author publishes items
	t.Log("=== Step 4: Author publishes items ===")
	item1 := testutil.PublishItem(t, authorToken,
		"Researchers at DeepMind published a new paper on chain-of-thought reasoning in large language models. The study demonstrates that structured prompting techniques can improve mathematical problem-solving accuracy by 40% compared to standard approaches. The team evaluated their method across multiple benchmarks including GSM8K and MATH.",
		"Significant advancement in LLM reasoning capabilities with real benchmark improvements",
		"https://example.com/ai-reasoning-paper")
	item1IDStr := item1["item_id"].(string)
	var item1ID int64
	fmt.Sscanf(item1IDStr, "%d", &item1ID)

	item2 := testutil.PublishItem(t, authorToken,
		"A team from MIT CSAIL released an open-source distributed consensus protocol that achieves 2x throughput compared to Raft in geo-distributed deployments. The protocol uses a novel quorum intersection technique that reduces cross-datacenter round trips while maintaining linearizability guarantees.",
		"New distributed systems consensus protocol with significant performance improvements",
		"https://example.com/distributed-consensus")
	item2IDStr := item2["item_id"].(string)
	var item2ID int64
	fmt.Sscanf(item2IDStr, "%d", &item2ID)

	// Step 5: Wait for items to be processed
	t.Log("=== Step 5: Wait for items processing ===")
	testutil.WaitForItemsProcessed(t, []int64{item1ID, item2ID})

	// Force ES refresh so newly indexed items become searchable
	// (ES refresh_interval is 30s, too slow for tests)
	testutil.RefreshES(t)

	// Step 6: User fetches feed
	t.Log("=== Step 6: User fetches feed ===")
	feedResp := testutil.FetchFeedRefresh(t, userToken, 20)
	items := feedResp["items"].([]interface{})
	t.Logf("Feed response: %+v", feedResp)
	t.Logf("Feed items count: %d", len(items))
	if len(items) == 0 {
		t.Fatal("expected at least 1 item in feed")
	}
	t.Logf("User fetched %d items", len(items))

	// Step 7: Submit feedback
	t.Log("=== Step 7: Submit feedback ===")
	// Only submit feedback for items that are in the feed
	feedbackItems := []map[string]interface{}{}
	for _, item := range items {
		itemMap := item.(map[string]interface{})
		itemIDStr := itemMap["item_id"].(string)
		var itemID int64
		fmt.Sscanf(itemIDStr, "%d", &itemID)
		if itemID == item1ID {
			feedbackItems = append(feedbackItems, map[string]interface{}{"item_id": itemIDStr, "score": 2})
		} else if itemID == item2ID {
			feedbackItems = append(feedbackItems, map[string]interface{}{"item_id": itemIDStr, "score": 1})
		}
	}
	if len(feedbackItems) == 0 {
		t.Fatal("no items to submit feedback for")
	}
	feedbackReq := map[string]interface{}{
		"items": feedbackItems,
	}
	data := testutil.SubmitFeedback(t, userToken, feedbackReq)
	processedCount := int(data["processed_count"].(float64))
	if processedCount != len(feedbackItems) {
		t.Fatalf("expected processed_count=%d, got %d", len(feedbackItems), processedCount)
	}
	t.Logf("Feedback submitted: %d items processed", processedCount)

	// Step 8: Check item stats
	t.Log("=== Step 8: Check item stats ===")
	// Only check stats for the first feedback item
	var firstFeedbackItemID int64
	if len(feedbackItems) > 0 {
		firstFeedbackItemID = testutil.MustID(t, feedbackItems[0]["item_id"], "item_id")
	}
	expectedScore := feedbackItems[0]["score"].(int)
	snapshot := testutil.WaitForItemStats(t, firstFeedbackItemID, 20*time.Second, func(stats testutil.ItemStatsSnapshot) bool {
		if expectedScore == 2 {
			return stats.Score2Count == 1 && stats.TotalScore == 2
		}
		return stats.Score1Count == 1 && stats.TotalScore == 1
	})
	if expectedScore == 2 && (snapshot.Score2Count != 1 || snapshot.TotalScore != 2) {
		t.Fatalf("expected score_2_count=1, total_score=2 for item %d, got score_2_count=%d, total_score=%d",
			firstFeedbackItemID, snapshot.Score2Count, snapshot.TotalScore)
	} else if expectedScore == 1 && (snapshot.Score1Count != 1 || snapshot.TotalScore != 1) {
		t.Fatalf("expected score_1_count=1, total_score=1 for item %d, got score_1_count=%d, total_score=%d",
			firstFeedbackItemID, snapshot.Score1Count, snapshot.TotalScore)
	}
	t.Logf("Item %d stats verified", firstFeedbackItemID)

	// Step 9: Query author's items
	t.Log("=== Step 9: Query author's items ===")
	myItemsResp := getMyItems(t, authorToken, 20)
	myItems := myItemsResp["items"].([]interface{})
	if len(myItems) != 2 {
		t.Fatalf("expected 2 items, got %d", len(myItems))
	}
	// Find item1 in response
	var foundItem map[string]interface{}
	for _, item := range myItems {
		itemMap := item.(map[string]interface{})
		if testutil.MustID(t, itemMap["item_id"], "item_id") == item1ID {
			foundItem = itemMap
			break
		}
	}
	if foundItem == nil {
		t.Fatalf("item %d not found in my items", item1ID)
	}
	if int64(foundItem["score_2_count"].(float64)) != 1 {
		t.Fatalf("expected score_2_count=1 in API response, got %v", foundItem["score_2_count"])
	}
	if got := foundItem["raw_content"]; got != "Researchers at DeepMind published a new paper on chain-of-thought reasoning in large language models. The study demonstrates that structured prompting techniques can improve mathematical problem-solving accuracy by 40% compared to standard approaches. The team evaluated their method across multiple benchmarks including GSM8K and MATH." {
		t.Fatalf("raw_content=%v, want original published content", got)
	}
	t.Logf("Author's items query successful, item %d has correct stats", item1ID)

	// Step 10: Check author's influence metrics
	t.Log("=== Step 10: Check author's influence metrics ===")
	authorInfo := testutil.GetAgent(t, authorToken)
	influence := authorInfo["influence"].(map[string]interface{})
	totalItems := int64(influence["total_items"].(float64))
	totalScored1 := int64(influence["total_scored_1"].(float64))
	totalScored2 := int64(influence["total_scored_2"].(float64))
	if totalItems != 2 {
		t.Fatalf("expected total_items=2, got %d", totalItems)
	}
	// Check that at least one score was recorded
	totalScored := totalScored1 + totalScored2
	if totalScored < 1 {
		t.Fatalf("expected at least 1 scored item, got total_scored_1=%d, total_scored_2=%d", totalScored1, totalScored2)
	}
	t.Logf("Author influence: total_items=%d, total_scored_1=%d, total_scored_2=%d",
		totalItems, totalScored1, totalScored2)

	t.Log("=== FEEDBACK FLOW TEST PASSED ===")
}

// getMyItems queries author's published items
func getMyItems(t *testing.T, token string, limit int) map[string]interface{} {
	t.Helper()
	url := baseURL + "/api/v1/agents/items"
	if limit > 0 {
		url += fmt.Sprintf("?limit=%d", limit)
	}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get my items request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if int(result["code"].(float64)) != 0 {
		t.Fatalf("get my items failed: %v", result["msg"])
	}
	return result["data"].(map[string]interface{})
}
