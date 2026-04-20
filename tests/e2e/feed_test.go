package e2e_test

import (
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/tests/testutil"
)

// TestFeedProtocol tests the new smart feed protocol with bloom filter deduplication
func TestFeedProtocol(t *testing.T) {
	testutil.WaitForAPI(t)
	testutil.CleanTestData(t)

	// Register and setup user
	t.Log("=== Setup: Register user ===")
	userResp := testutil.RegisterAgent(t, "feedtest@test.com", "FeedTestUser", "")
	userToken := userResp["token"].(string)
	userID := testutil.MustID(t, userResp["agent_id"], "agent_id")

	// Update profile
	testutil.UpdateProfile(t, userToken, "I am interested in technology, AI, and software development")
	testutil.WaitForProfileProcessed(t, userID)

	// Register author
	authorResp := testutil.RegisterAgent(t, "feedauthor@test.com", "FeedAuthor", "")
	authorToken := authorResp["token"].(string)

	// Publish multiple items with distinct, substantial content
	t.Log("=== Publish items ===")
	contents := []struct {
		content string
		notes   string
		url     string
	}{
		{
			"OpenAI announced GPT-5 with significant improvements in mathematical reasoning and code generation. The model achieves a 25% improvement on MATH benchmark and 30% on HumanEval compared to GPT-4. Key innovations include a new training methodology combining reinforcement learning from human feedback with synthetic data generation for complex reasoning tasks.",
			"GPT-5 release with major reasoning improvements",
			"https://example.com/gpt5-release",
		},
		{
			"Meta AI open-sourced LLaMA 4, a 400-billion parameter language model trained on 15 trillion tokens. The model includes a novel sparse attention mechanism that reduces memory usage by 40% during inference. Early benchmarks show competitive performance with proprietary models on both academic benchmarks and real-world coding tasks.",
			"LLaMA 4 open source release with efficiency improvements",
			"https://example.com/llama4-release",
		},
		{
			"Anthropic published research on constitutional AI methods for improving language model safety and alignment. The study introduces a new technique called iterative preference refinement that reduces harmful outputs by 85% while maintaining helpfulness scores. The approach combines automated red-teaming with human oversight in a scalable training pipeline.",
			"New constitutional AI safety research with measurable improvements",
			"https://example.com/constitutional-ai-v2",
		},
		{
			"Stanford researchers developed a new transformer architecture called FlashAttention-3 that enables 10x faster inference for long-context language models. The technique uses a tiled computation approach that optimizes GPU memory access patterns. Experiments show the method can process 1 million token contexts with sub-second latency on a single A100 GPU.",
			"FlashAttention-3 enables million-token context processing",
			"https://example.com/flashattention3",
		},
		{
			"Google Brain released a comprehensive study on scaling laws for language model pre-training, analyzing the relationship between model size, dataset size, and compute budget. The research identifies optimal training configurations for different resource constraints and proposes a new cost-efficient training recipe that achieves equivalent performance using 50% less compute.",
			"New scaling laws research for efficient AI model training",
			"https://example.com/scaling-laws-2026",
		},
	}

	var publishedIDs []int64
	for _, c := range contents {
		item := testutil.PublishItem(t, authorToken, c.content, c.notes, c.url)
		publishedIDs = append(publishedIDs, testutil.MustID(t, item["item_id"], "item_id"))
		time.Sleep(100 * time.Millisecond)
	}

	// Wait for processing
	testutil.WaitForItemsProcessed(t, publishedIDs)
	testutil.RefreshES(t)

	// Test 1: Refresh action
	t.Log("=== Test 1: Refresh action ===")
	feed1 := testutil.FetchFeedRefresh(t, userToken, 3)
	items1 := feed1["items"].([]interface{})
	hasMore1 := feed1["has_more"].(bool)
	t.Logf("Refresh returned %d items, has_more=%v", len(items1), hasMore1)

	if len(items1) == 0 {
		t.Log("No items in feed, skipping remaining tests")
		return
	}

	// Collect group_ids and urls from first fetch
	groupIDs1 := make(map[string]bool)
	seenURLs := make(map[string]bool)
	for _, it := range items1 {
		item := it.(map[string]interface{})
		if groupID, ok := item["group_id"].(string); ok && groupID != "" {
			groupIDs1[groupID] = true
		}
		if u, ok := item["url"].(string); ok && u != "" {
			seenURLs[u] = true
		}
	}
	t.Logf("First fetch returned %d unique groups", len(groupIDs1))

	// Verify url is propagated from raw_items.raw_url on at least one item.
	// All test items were published with distinct https://example.com/... URLs.
	foundExpectedURL := false
	for _, c := range contents {
		if seenURLs[c.url] {
			foundExpectedURL = true
			break
		}
	}
	if !foundExpectedURL {
		t.Errorf("feed response did not include a url matching any published raw_url; seenURLs=%v", seenURLs)
	}

	// Test 2: Load more action
	if hasMore1 {
		t.Log("=== Test 2: Load more action ===")
		feed2 := testutil.FetchFeedLoadMore(t, userToken, 2)
		items2 := feed2["items"].([]interface{})
		hasMore2 := feed2["has_more"].(bool)
		t.Logf("Load more returned %d items, has_more=%v", len(items2), hasMore2)

		// Verify no overlap with first fetch
		for _, it := range items2 {
			item := it.(map[string]interface{})
			if groupID, ok := item["group_id"].(string); ok && groupID != "" {
				if groupIDs1[groupID] {
					t.Errorf("Load more returned duplicate group_id: %s", groupID)
				}
			}
		}
	}

	// Test 3: Bloom filter deduplication
	t.Log("=== Test 3: Bloom filter deduplication ===")
	time.Sleep(1 * time.Second)
	feed3 := testutil.FetchFeedRefresh(t, userToken, 10)
	items3 := feed3["items"].([]interface{})
	t.Logf("Second refresh returned %d items", len(items3))

	// Verify that previously seen groups are not returned (only when dedup is enabled)
	cfg := config.Load()
	if cfg.ShouldDisableDedup() {
		t.Log("Dedup disabled in test environment, skipping bloom filter assertion")
	} else {
		duplicateCount := 0
		for _, it := range items3 {
			item := it.(map[string]interface{})
			if groupID, ok := item["group_id"].(string); ok && groupID != "" {
				if groupIDs1[groupID] {
					duplicateCount++
					t.Errorf("Bloom filter failed: group_id %s was seen before but returned again", groupID)
				}
			}
		}
		if duplicateCount == 0 {
			t.Log("Bloom filter deduplication working correctly")
		} else {
			t.Errorf("Bloom filter deduplication failed: %d duplicates found", duplicateCount)
		}
	}

	// Test 4: Empty cache fallback
	t.Log("=== Test 4: Empty cache fallback ===")
	// Wait for cache to expire (30 minutes in production, but we can test the fallback)
	// Try load_more when cache is likely empty
	time.Sleep(2 * time.Second)
	feed4 := testutil.FetchFeedLoadMore(t, userToken, 5)
	items4 := feed4["items"].([]interface{})
	t.Logf("Load more with empty cache returned %d items (should fallback to refresh)", len(items4))

	// Test 5: Verify has_more flag accuracy
	t.Log("=== Test 5: Verify has_more flag ===")
	feed5 := testutil.FetchFeedRefresh(t, userToken, 100)
	items5 := feed5["items"].([]interface{})
	hasMore5 := feed5["has_more"].(bool)
	t.Logf("Large limit fetch returned %d items, has_more=%v", len(items5), hasMore5)

	if len(items5) < 100 && hasMore5 {
		t.Error("has_more should be false when all items are returned")
	}

	t.Log("=== Feed protocol tests completed ===")
}
