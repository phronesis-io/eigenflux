package console_test

import (
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

const outboundStatsEmail = "console-outbound-stats@test.com"

// seedItemStats inserts a raw_items + item_stats pair directly, bypassing the
// LLM pipeline, so the /console/today aggregation can be asserted exactly.
func seedItemStats(t *testing.T, itemID, agentID, createdAtMs, score1, score2 int64) {
	t.Helper()
	if _, err := testutil.TestDB.Exec(
		`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES ($1, $2, 'outbound stats seed', $3)`,
		itemID, agentID, createdAtMs,
	); err != nil {
		t.Fatalf("failed to seed raw_items: %v", err)
	}
	if _, err := testutil.TestDB.Exec(
		`INSERT INTO item_stats (item_id, author_agent_id, consumed_count, score_1_count, score_2_count, created_at, updated_at)
		 VALUES ($1, $2, 5, $3, $4, $5, $5)`,
		itemID, agentID, score1, score2, createdAtMs,
	); err != nil {
		t.Fatalf("failed to seed item_stats: %v", err)
	}
	t.Cleanup(func() {
		// item_stats cascades from raw_items.
		_, _ = testutil.TestDB.Exec(`DELETE FROM raw_items WHERE item_id = $1`, itemID)
	})
}

// TestConsoleTodayOutboundMatchesMyItems verifies the outbound card counts
// item_stats rows in the 7-day window and sums score_1 + score_2, matching
// the praise_count shown per item on the "my broadcasts" list.
func TestConsoleTodayOutboundMatchesMyItems(t *testing.T) {
	testutil.WaitForAPI(t)
	token, agentID, _ := testutil.LoginAndGetToken(t, outboundStatsEmail)

	nowMs := time.Now().UnixMilli()
	// Negative IDs cannot collide with idgen-issued item IDs.
	base := -nowMs
	// Two items inside the 7-day window: score_1=2/score_2=1 and score_1=0/score_2=3.
	seedItemStats(t, base, agentID, nowMs-1*3600*1000, 2, 1)
	seedItemStats(t, base+1, agentID, nowMs-24*3600*1000, 0, 3)
	// One item outside the window: must not be counted.
	seedItemStats(t, base+2, agentID, nowMs-8*24*3600*1000, 7, 7)

	result := testutil.DoGet(t, "/api/v1/console/today", token)
	assertCode(t, result, 0)

	data := result["data"].(map[string]interface{})
	today := data["today"].(map[string]interface{})
	outbound := today["outbound"].(map[string]interface{})

	if got := int64(outbound["broadcasts_sent"].(float64)); got != 2 {
		t.Errorf("broadcasts_sent: expected 2 items in 7-day window, got %d", got)
	}
	// them_marked_useful = score_1 + score_2 over the window = (2+1) + (0+3).
	if got := int64(outbound["them_marked_useful"].(float64)); got != 6 {
		t.Errorf("them_marked_useful: expected 6 (score_1+score_2 in window), got %d", got)
	}
	if got := int64(outbound["total_reach"].(float64)); got != 10 {
		t.Errorf("total_reach: expected 10 (consumed_count in window), got %d", got)
	}
}
