package e2e_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/tests/testutil"
)

func TestMain(m *testing.M) {
	testutil.RunTestMain(m)
}

func logJSONResponse(t *testing.T, label string, resp interface{}) {
	t.Helper()
	pretty, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		t.Logf("[%s] failed to pretty-print response: %v, raw=%#v", label, err, resp)
		return
	}
	t.Logf("[%s] response JSON:\n%s", label, string(pretty))
}

func logRawHTTPBody(t *testing.T, label string, body []byte) {
	t.Helper()
	var parsed interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Logf("[%s] response body (non-JSON):\n%s", label, string(body))
		return
	}
	logJSONResponse(t, label, parsed)
}

func TestE2EFullFlow(t *testing.T) {
	testutil.WaitForAPI(t)
	testutil.CleanTestData(t)

	// Step 1: Register Author Agent
	t.Log("=== Step 1: Register Author Agent ===")
	authorResp := testutil.RegisterAgent(t, "author@test.com", "AuthorBot", "I write about AI and technology")
	logJSONResponse(t, "Step1.RegisterAuthor", authorResp)
	authorToken := authorResp["token"].(string)
	authorID := testutil.MustID(t, authorResp["agent_id"], "agent_id")
	t.Logf("Author registered: id=%d, token=%s", authorID, authorToken[:8]+"...")

	// Step 2: Register User Agent
	t.Log("=== Step 2: Register User Agent ===")
	userResp := testutil.RegisterAgent(t, "user@test.com", "UserBot", "")
	logJSONResponse(t, "Step2.RegisterUser", userResp)
	userToken := userResp["token"].(string)
	userID := testutil.MustID(t, userResp["agent_id"], "agent_id")
	t.Logf("User registered: id=%d, token=%s", userID, userToken[:8]+"...")

	// Step 3: User updates profile with interests
	t.Log("=== Step 3: User updates profile ===")
	testutil.UpdateProfile(t, userToken, "I am interested in artificial intelligence, machine learning, large language models, and distributed systems")

	// Step 4: Wait for pipeline to process profile
	t.Log("=== Step 4: Wait for profile pipeline processing ===")
	testutil.WaitForProfileProcessed(t, userID)

	agentInfo := testutil.GetAgent(t, userToken)
	logJSONResponse(t, "Step4.GetAgent", agentInfo)
	profile := agentInfo["profile"].(map[string]interface{})
	if profile["agent_name"].(string) != "UserBot" {
		t.Fatalf("expected agent_name=UserBot, got %v", profile["agent_name"])
	}
	if profile["bio"].(string) == "" {
		t.Fatal("expected non-empty bio after profile update")
	}
	t.Logf("User profile ready: agent_name=%v", profile["agent_name"])

	// Step 5: Author publishes items
	t.Log("=== Step 5: Author publishes items ===")
	item1 := testutil.PublishItem(t, authorToken,
		"Google DeepMind released Gemini 2.0, a next-generation multimodal AI model that achieves state-of-the-art performance across text, image, and code understanding tasks. The model introduces a new mixture-of-experts architecture that reduces inference costs by 60% while maintaining quality. Benchmarks show significant improvements on MMLU (+8%), HumanEval (+15%), and visual QA tasks compared to previous generation models.",
		"Major advancement in multimodal AI with improved efficiency and benchmark results",
		"https://example.com/gemini-2-release")
	logJSONResponse(t, "Step5.PublishItem1", item1)
	item1ID := testutil.MustID(t, item1["item_id"], "item_id")

	item2 := testutil.PublishItem(t, authorToken,
		"The Cloud Native Computing Foundation announced Kubernetes 1.32 with major improvements to pod scheduling and resource management. The release introduces a new priority-based preemption algorithm that reduces scheduling latency by 40% in clusters with over 10,000 nodes. Additionally, the updated HPA controller now supports custom metrics from multiple sources, enabling more sophisticated autoscaling strategies for microservices architectures.",
		"Kubernetes 1.32 brings significant scheduling and autoscaling improvements for large-scale deployments",
		"https://example.com/k8s-132-release")
	logJSONResponse(t, "Step5.PublishItem2", item2)
	item2ID := testutil.MustID(t, item2["item_id"], "item_id")

	item3 := testutil.PublishItem(t, authorToken,
		"A renowned chef from Florence documented traditional Tuscan pasta-making techniques passed down through three generations. The guide covers hand-rolled pici, pappardelle, and tagliatelle with detailed instructions on flour selection, dough hydration ratios, and regional sauce pairings. The project aims to preserve artisanal Italian culinary heritage in an era of industrialized food production.",
		"Comprehensive guide to traditional Tuscan pasta techniques preserving culinary heritage",
		"https://example.com/tuscan-pasta-guide")
	logJSONResponse(t, "Step5.PublishItem3", item3)
	item3ID := testutil.MustID(t, item3["item_id"], "item_id")

	// Step 6: Wait for pipeline to process all items
	t.Log("=== Step 6: Wait for item pipeline processing ===")
	testutil.WaitForItemsProcessed(t, []int64{item1ID, item2ID, item3ID})
	testutil.RefreshES(t)

	// Step 7: User fetches feed (refresh)
	// Use WaitForFeedMinItems to handle ES indexing delay (pipeline may still be indexing after DB status=3)
	t.Log("=== Step 7: User fetches feed (refresh) ===")
	items := testutil.WaitForFeedMinItems(t, userToken, 1, 30*time.Second)
	t.Logf("Feed returned %d items", len(items))
	hasMore := false // will be checked in load_more step
	foundTech := false
	foundPasta := false
	for _, it := range items {
		item := it.(map[string]interface{})
		summary := strings.ToLower(item["summary"].(string))
		t.Logf("  Feed item: id=%v, summary=%s", item["item_id"], item["summary"])
		if strings.Contains(summary, "llm") || strings.Contains(summary, "language model") ||
			strings.Contains(summary, "kubernetes") || strings.Contains(summary, "distributed") ||
			strings.Contains(summary, "ai") || strings.Contains(summary, "gemini") ||
			strings.Contains(summary, "deepmind") || strings.Contains(summary, "multimodal") {
			foundTech = true
		}
		if strings.Contains(summary, "pasta") || strings.Contains(summary, "recipe") || strings.Contains(summary, "tuscan") {
			foundPasta = true
		}
	}
	if !foundTech {
		t.Error("expected at least one tech-related article in feed")
	}
	if foundPasta {
		t.Error("did NOT expect pasta/recipe article in feed for an AI-interested user")
	}

	// Step 8: Test item detail API
	t.Log("=== Step 8: Test item detail API ===")
	firstFeedItem := items[0].(map[string]interface{})
	firstItemID := testutil.MustID(t, firstFeedItem["item_id"], "item_id")
	itemDetail := testutil.GetItem(t, userToken, firstItemID)
	logJSONResponse(t, "Step8.GetItem", itemDetail)
	t.Logf("Item detail: id=%v, broadcast_type=%v", itemDetail["item_id"], itemDetail["broadcast_type"])
	if testutil.MustID(t, itemDetail["item_id"], "item_id") != firstItemID {
		t.Fatalf("expected item_id=%d, got %v", firstItemID, itemDetail["item_id"])
	}
	if _, ok := itemDetail["broadcast_type"]; !ok {
		t.Error("expected broadcast_type in item detail")
	}
	if _, ok := itemDetail["content"]; !ok {
		t.Error("expected content in item detail")
	}
	if _, ok := itemDetail["updated_at"]; !ok {
		t.Error("expected updated_at in item detail")
	}

	// Step 9: Test load_more action
	t.Log("=== Step 9: Test load_more action ===")
	if hasMore {
		feed2 := testutil.FetchFeedLoadMore(t, userToken, 20)
		logJSONResponse(t, "Step9.FetchFeedLoadMore", feed2)
		items2 := feed2["items"].([]interface{})
		t.Logf("Load more returned %d items", len(items2))
	} else {
		t.Log("No more items to load, skipping load_more test")
	}

	// Step 10: Test bloom filter deduplication
	t.Log("=== Step 10: Test bloom filter deduplication ===")
	time.Sleep(2 * time.Second)
	dedupeFeed := testutil.FetchFeedRefresh(t, userToken, 20)
	logJSONResponse(t, "Step10.FetchFeedDedupe", dedupeFeed)
	dedupeItems := dedupeFeed["items"].([]interface{})
	t.Logf("Deduplication feed returned %d items", len(dedupeItems))

	// Build a map of seen group_ids from first fetch
	seenGroupIDs := make(map[string]bool)
	for _, it := range items {
		item := it.(map[string]interface{})
		if groupID, ok := item["group_id"].(string); ok && groupID != "" {
			seenGroupIDs[groupID] = true
		}
	}

	// Check that dedupe feed doesn't contain previously seen group_ids
	// (only when dedup is enabled; DISABLE_DEDUP_IN_TEST=true skips this check)
	cfg := config.Load()
	if cfg.ShouldDisableDedup() {
		t.Log("Dedup disabled in test environment, skipping dedup assertion")
	} else {
		for _, it := range dedupeItems {
			item := it.(map[string]interface{})
			if groupID, ok := item["group_id"].(string); ok && groupID != "" {
				if seenGroupIDs[groupID] {
					t.Errorf("expected group_id %s to be deduplicated (already seen), but it appeared again", groupID)
				}
			}
		}
	}
	t.Logf("Deduplication check: %d groups seen previously, %d items in deduped feed", len(seenGroupIDs), len(dedupeItems))

	// Step 11: Verify SKILL.md is accessible
	t.Log("=== Step 11: Verify SKILL.md ===")
	resp, err := http.Get(testutil.BaseURL + "/skill.md")
	if err != nil {
		t.Fatalf("failed to fetch skill.md: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	t.Logf("[Step11.GetSkill] status=%d", resp.StatusCode)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 for skill.md, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), config.Load().ProjectTitle) {
		t.Fatalf("skill.md does not contain expected project title %q", config.Load().ProjectTitle)
	}

	// Step 12: Test auth - unauthorized access
	t.Log("=== Step 12: Test unauthorized access ===")
	req, _ := http.NewRequest("GET", testutil.BaseURL+"/api/v1/items/feed", nil)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp2.Body.Close()
	unauthorizedBody, _ := io.ReadAll(resp2.Body)
	t.Logf("[Step12.UnauthorizedFeed] status=%d", resp2.StatusCode)
	logRawHTTPBody(t, "Step12.UnauthorizedFeed", unauthorizedBody)
	if resp2.StatusCode != 401 {
		t.Fatalf("expected 401 for unauthorized access, got %d", resp2.StatusCode)
	}

	t.Log("=== ALL E2E TESTS PASSED ===")
}

func TestGetItemErrorCases(t *testing.T) {
	// The published content is reused across e2e tests (also appears in
	// TestSortContextFeaturesInReplayLog); without a clean the second test to
	// run sees an exact-hash duplicate and is discarded by the dedup step.
	testutil.CleanTestData(t)
	timestamp := time.Now().UnixNano()
	authorEmail := fmt.Sprintf("error_test_%d@test.com", timestamp)
	authorResp := testutil.RegisterAgent(t, authorEmail, "ErrorAuthor", "Test author")
	authorToken := authorResp["token"].(string)

	publishResp := testutil.PublishItem(t, authorToken,
		"Microsoft Research released a comprehensive study on retrieval-augmented generation (RAG) techniques for enterprise knowledge management. The paper evaluates 12 different chunking strategies across 5 embedding models and finds that semantic chunking with overlap produces 35% better recall than fixed-size approaches. The study also introduces a novel hybrid retrieval pipeline combining dense and sparse retrievers that achieves state-of-the-art performance on domain-specific QA benchmarks.",
		"RAG techniques study with practical enterprise recommendations", "")
	logJSONResponse(t, "GetItemErrorCases.PublishItem", publishResp)
	itemID := testutil.MustID(t, publishResp["item_id"], "item_id")

	testutil.WaitForItemsProcessed(t, []int64{itemID})

	t.Run("GetItem_InvalidItemID", func(t *testing.T) {
		req, _ := http.NewRequest("GET", testutil.BaseURL+"/api/v1/items/invalid", nil)
		req.Header.Set("Authorization", "Bearer "+authorToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		t.Logf("[GetItem_InvalidItemID] status=%d", resp.StatusCode)
		logRawHTTPBody(t, "GetItem_InvalidItemID", body)
		if resp.StatusCode != 400 {
			t.Errorf("expected 400 for invalid item_id, got %d", resp.StatusCode)
		}
	})

	t.Run("GetItem_NonExistentItem", func(t *testing.T) {
		resp := testutil.DoGet(t, "/api/v1/items/999999", authorToken)
		logJSONResponse(t, "GetItem_NonExistentItem", resp)
		code := int(resp["code"].(float64))
		if code != 404 {
			t.Errorf("expected code 404 for non-existent item, got %d", code)
		}
	})

	t.Run("GetItem_Unauthorized", func(t *testing.T) {
		req, _ := http.NewRequest("GET", testutil.BaseURL+"/api/v1/items/"+fmt.Sprintf("%d", itemID), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		t.Logf("[GetItem_Unauthorized] status=%d", resp.StatusCode)
		logRawHTTPBody(t, "GetItem_Unauthorized", body)
		if resp.StatusCode != 401 {
			t.Errorf("expected 401 for unauthorized access, got %d", resp.StatusCode)
		}
	})

	t.Run("GetItem_AllFieldsPresent", func(t *testing.T) {
		itemDetail := testutil.GetItem(t, authorToken, itemID)
		logJSONResponse(t, "GetItem_AllFieldsPresent", itemDetail)
		requiredFields := []string{
			"item_id", "broadcast_type", "content", "updated_at",
			"summary", "domains", "keywords",
		}
		for _, field := range requiredFields {
			if _, ok := itemDetail[field]; !ok {
				t.Errorf("expected field %s in item detail, but not found", field)
			}
		}
		optionalFields := []string{
			"expire_time", "geo", "source_type", "expected_response",
			"group_id", "url",
		}
		for _, field := range optionalFields {
			if _, ok := itemDetail[field]; !ok {
				t.Logf("optional field %s not present in response", field)
			}
		}
	})

	t.Log("=== ALL ERROR CASE TESTS PASSED ===")
}
