package item_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

func TestBlacklistItemDiscard(t *testing.T) {
	testutil.WaitForAPI(t)
	testutil.CleanTestData(t)

	seed := time.Now().UnixNano() % 1_000_000_000
	blacklistWord := fmt.Sprintf("XBLOCKTEST%d", seed)

	// 1. Create blacklist keyword via console API
	type BlacklistResp struct {
		Code int32 `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			Keyword struct {
				KeywordID string `json:"keyword_id"`
			} `json:"keyword"`
		} `json:"data"`
	}
	createPayload := testutil.DoConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/blacklist-keywords", map[string]interface{}{
		"keyword": blacklistWord,
	})
	var created BlacklistResp
	testutil.MustDecodeResp(t, createPayload, &created)
	if created.Code != 0 || created.Data == nil {
		t.Fatalf("failed to create blacklist keyword: code=%d msg=%s", created.Code, created.Msg)
	}
	keywordID := created.Data.Keyword.KeywordID
	t.Logf("Created blacklist keyword %q (id=%s)", blacklistWord, keywordID)

	// Cleanup keyword after test
	defer func() {
		testutil.DoConsoleJSONRequest(t, http.MethodDelete, "/console/api/v1/blacklist-keywords/"+keywordID, nil)
		t.Logf("Cleaned up blacklist keyword %s", keywordID)
	}()

	// 2. Register author
	author := testutil.RegisterAgent(t, "blacklist-author@test.com", "BlacklistTestBot", "I test blacklist")
	token := author["token"].(string)

	// 3. Publish item A: contains blacklist keyword → should be discarded (status=4)
	contentBlocked := fmt.Sprintf("Researchers at Stanford University published a comprehensive analysis of %s technology adoption across Fortune 500 companies. The study reveals that enterprises implementing %s-based solutions saw a 45%% reduction in operational costs and 3x improvement in processing throughput over a 12-month period.", blacklistWord, blacklistWord)
	itemA := testutil.PublishItem(t, token, contentBlocked,
		"Enterprise technology adoption study with quantitative findings",
		"https://example.com/enterprise-study")
	itemAID := testutil.MustID(t, itemA["item_id"], "item_id")
	t.Logf("Published blacklisted item A: %d", itemAID)

	// 4. Publish item B: normal content → should be processed (status=3)
	contentClean := "Google DeepMind released a new framework for multi-agent reinforcement learning that enables autonomous agents to collaborate on complex tasks. The system uses a novel communication protocol that reduces coordination overhead by 60%% while maintaining task completion rates. Benchmarks on StarCraft II and robotic manipulation tasks show significant improvements over previous approaches, with agents learning cooperative strategies in half the training time."
	itemB := testutil.PublishItem(t, token, contentClean,
		"Multi-agent reinforcement learning framework with significant benchmark improvements",
		"https://example.com/multi-agent-rl")
	itemBID := testutil.MustID(t, itemB["item_id"], "item_id")
	t.Logf("Published clean item B: %d", itemBID)

	// 5. Wait for item A to be discarded (status=4)
	testutil.WaitForItemStatus(t, itemAID, 4, 120*time.Second)
	t.Logf("Item A (%d) correctly discarded by blacklist", itemAID)

	// 6. Wait for item B to be processed normally (status=3)
	testutil.WaitForItemStatus(t, itemBID, 3, 120*time.Second)
	t.Logf("Item B (%d) correctly processed", itemBID)
}
