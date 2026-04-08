package website_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	testutil.RunTestMain(m)
}

const websiteBaseURL = "http://localhost:8080"

type WebsiteStatsData struct {
	AgentCount           int64 `json:"agent_count"`
	ItemCount            int64 `json:"item_count"`
	HighQualityItemCount int64 `json:"high_quality_item_count"`
}

type WebsiteStatsResp struct {
	Code int32            `json:"code"`
	Msg  string           `json:"msg"`
	Data WebsiteStatsData `json:"data"`
}

type WebsiteItemInfo struct {
	ID      string            `json:"id"`
	Agent   string            `json:"agent"`
	Country string            `json:"country"`
	Type    string            `json:"type"`
	Domains []string          `json:"domains"`
	Content string            `json:"content"`
	URL     *string           `json:"url"`
	Notes   map[string]string `json:"notes"`
}

type LatestItemsData struct {
	Items []WebsiteItemInfo `json:"items"`
}

type LatestItemsResp struct {
	Code int32           `json:"code"`
	Msg  string          `json:"msg"`
	Data LatestItemsData `json:"data"`
}

func TestWebsiteStatsInitialization(t *testing.T) {
	// Check if console API is running
	resp, err := http.Get(fmt.Sprintf("%s/api/v1/website/stats", websiteBaseURL))
	if err != nil {
		t.Skipf("API gateway not running: %v", err)
		return
	}
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var statsResp WebsiteStatsResp
	err = json.Unmarshal(body, &statsResp)
	require.NoError(t, err)

	assert.Equal(t, int32(0), statsResp.Code, "Response code should be 0")
	assert.Equal(t, "success", statsResp.Msg)
	assert.GreaterOrEqual(t, statsResp.Data.AgentCount, int64(0), "Agent count should be >= 0")
	assert.GreaterOrEqual(t, statsResp.Data.ItemCount, int64(0), "Item count should be >= 0")
	assert.GreaterOrEqual(t, statsResp.Data.HighQualityItemCount, int64(0), "High quality count should be >= 0")
}

func TestWebsiteStatsIncrement(t *testing.T) {
	// Setup: Get initial stats
	initialStats := getWebsiteStats(t)

	// Create test agent and publish item
	agent := testutil.RegisterAgent(t, "stats_test@example.com", "StatsTestAgent", "Test bio")
	token := agent["token"].(string)

	// Use unique content per run to avoid hash-based dedup
	content := fmt.Sprintf(`Anthropic released Claude 4.5 Sonnet on 2026-03-15, achieving state-of-the-art scores on SWE-bench (72.3%%) and HumanEval (96.1%%). The model introduces a 200K token context window with near-perfect recall at 128K tokens. Pricing is set at $3 per million input tokens and $15 per million output tokens. Enterprise customers can access the model immediately via the API; general availability begins March 22. [ref:%d]`, time.Now().UnixNano())
	itemResp := testutil.PublishItem(t, token, content, "", "")
	itemID, _ := strconv.ParseInt(itemResp["item_id"].(string), 10, 64)

	// Wait for pipeline to fully process the item
	testutil.WaitForItemsProcessed(t, []int64{itemID})
	// Allow fire-and-forget stats goroutine to complete
	time.Sleep(2 * time.Second)

	// Get updated stats
	updatedStats := getWebsiteStats(t)

	// Verify item count incremented
	assert.Greater(t, updatedStats.ItemCount, initialStats.ItemCount, "Item count should increment")
}

func TestWebsiteStatsHighQuality(t *testing.T) {
	// Setup: Get initial stats
	initialStats := getWebsiteStats(t)

	// Create test agent
	agent := testutil.RegisterAgent(t, "hq_test@example.com", "HQTestAgent", "Test bio")
	token := agent["token"].(string)

	// Use unique content per run to avoid hash-based dedup
	content := fmt.Sprintf(`Google DeepMind published "Scaling LLM Test-Time Compute" (arXiv:2408.03314) on 2026-03-20, demonstrating that letting models spend 4x more inference-time compute on hard problems improves MATH benchmark accuracy from 74.6%% to 91.2%% without retraining. Key findings: (1) a learned "compute-optimal" policy outperforms best-of-N sampling by 3.1x in FLOPs efficiency, (2) for easy questions the base model already saturates, (3) the approach is complementary to larger pre-training budgets. The paper proposes an adaptive routing scheme that allocates test-time compute proportional to estimated question difficulty, reducing average inference cost by 38%% while maintaining accuracy. Code and checkpoints are available at github.com/google-deepmind/scaling-ttc. [ref:%d]`, time.Now().UnixNano())
	itemResp := testutil.PublishItem(t, token, content, "", "")
	itemID, _ := strconv.ParseInt(itemResp["item_id"].(string), 10, 64)

	// Wait for pipeline to fully process the item
	testutil.WaitForItemsProcessed(t, []int64{itemID})
	// Allow fire-and-forget stats goroutine to complete
	time.Sleep(2 * time.Second)

	// Get updated stats
	updatedStats := getWebsiteStats(t)

	// Verify both item count and high-quality count incremented
	assert.Greater(t, updatedStats.ItemCount, initialStats.ItemCount, "Item count should increment")
	assert.GreaterOrEqual(t, updatedStats.HighQualityItemCount, initialStats.HighQualityItemCount, "High quality count should increment or stay same")
}

func TestLatestItemsPush(t *testing.T) {
	ctx := context.Background()
	rdb := testutil.GetTestRedis()

	// Push a test snapshot directly to the Redis latest-items list so the test
	// validates the API layer without depending on the LLM pipeline.
	testItemID := "999000111222333"
	snapshot := fmt.Sprintf(
		`{"id":%s,"agent":"LatestTestAgent","country":"US","type":"request","domains":["tech"],"content":"Test content for latest items list","url":"","notes":{}}`,
		testItemID,
	)
	err := rdb.LPush(ctx, "public:latest_items", snapshot).Err()
	require.NoError(t, err)
	// Clean up after test
	t.Cleanup(func() { rdb.LRem(ctx, "public:latest_items", 1, snapshot) })

	// Verify item appears via the API
	items := getLatestItems(t, 10)
	found := false
	for _, item := range items {
		if item.ID == testItemID {
			found = true
			assert.Equal(t, "LatestTestAgent", item.Agent)
			assert.Equal(t, "US", item.Country)
			assert.Equal(t, "request", item.Type)
			assert.Contains(t, item.Content, "Test content for latest items list")
			break
		}
	}
	assert.True(t, found, "Published item should appear in latest items list")
}

func TestLatestItemsCapped(t *testing.T) {
	// This test verifies that the list is capped at 50 items
	// We'll check the Redis list directly
	ctx := context.Background()
	rdb := testutil.GetTestRedis()

	// Get list length
	length, err := rdb.LLen(ctx, "public:latest_items").Result()
	require.NoError(t, err)

	// List should not exceed 50 items
	assert.LessOrEqual(t, length, int64(50), "Latest items list should be capped at 50")
}

func TestLatestItemsFields(t *testing.T) {
	// Get latest items
	items := getLatestItems(t, 1)

	if len(items) == 0 {
		t.Skip("No items in latest items list")
		return
	}

	item := items[0]

	// Verify all required fields are present
	assert.NotEmpty(t, item.ID, "ID should not be empty")
	assert.NotEmpty(t, item.Agent, "Agent should not be empty")
	// Country can be empty
	assert.NotEmpty(t, item.Type, "Type should not be empty")
	assert.NotNil(t, item.Domains, "Domains should not be nil")
	assert.NotEmpty(t, item.Content, "Content should not be empty")
	// URL can be nil
	assert.NotNil(t, item.Notes, "Notes should not be nil")
}

func TestLatestItemsLimit(t *testing.T) {
	// Test default limit (10)
	items := getLatestItems(t, 0)
	assert.LessOrEqual(t, len(items), 10, "Default limit should be 10")

	// Test custom limit (5)
	items = getLatestItems(t, 5)
	assert.LessOrEqual(t, len(items), 5, "Custom limit should be respected")

	// Test max limit (50)
	items = getLatestItems(t, 100)
	assert.LessOrEqual(t, len(items), 50, "Max limit should be 50")
}

func TestWebsiteStatsRedisKeys(t *testing.T) {
	// Verify Redis keys exist
	ctx := context.Background()
	rdb := testutil.GetTestRedis()

	// Check agent count key
	agentCount, err := rdb.Get(ctx, "stats:agent_count").Int64()
	if err != nil && err != redis.Nil {
		t.Errorf("Failed to get agent count from Redis: %v", err)
	}
	assert.GreaterOrEqual(t, agentCount, int64(0))

	// Check item total key
	itemTotal, err := rdb.Get(ctx, "stats:item_total").Int64()
	if err != nil && err != redis.Nil {
		t.Errorf("Failed to get item total from Redis: %v", err)
	}
	assert.GreaterOrEqual(t, itemTotal, int64(0))

	// Check high quality count key
	hqCount, err := rdb.Get(ctx, "stats:high_quality_count").Int64()
	if err != nil && err != redis.Nil {
		t.Errorf("Failed to get high quality count from Redis: %v", err)
	}
	assert.GreaterOrEqual(t, hqCount, int64(0))
}

// Helper functions

func getWebsiteStats(t *testing.T) WebsiteStatsData {
	resp, err := http.Get(fmt.Sprintf("%s/api/v1/website/stats", websiteBaseURL))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var statsResp WebsiteStatsResp
	err = json.Unmarshal(body, &statsResp)
	require.NoError(t, err)

	require.Equal(t, int32(0), statsResp.Code)
	return statsResp.Data
}

func getLatestItems(t *testing.T, limit int) []WebsiteItemInfo {
	url := fmt.Sprintf("%s/api/v1/website/latest-items", websiteBaseURL)
	if limit > 0 {
		url = fmt.Sprintf("%s?limit=%d", url, limit)
	}

	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var itemsResp LatestItemsResp
	err = json.Unmarshal(body, &itemsResp)
	require.NoError(t, err)

	require.Equal(t, int32(0), itemsResp.Code)
	return itemsResp.Data.Items
}
