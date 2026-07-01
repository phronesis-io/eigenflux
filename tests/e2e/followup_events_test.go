package e2e_test

import (
	"fmt"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

// TestPushFeedEvents covers the follow-up label ingestion path end-to-end:
// authenticated batch POST -> stream -> pipeline consumer -> followup_labels.
func TestPushFeedEvents(t *testing.T) {
	testutil.WaitForAPI(t)

	const email = "followup-author@test.com"
	testutil.CleanupTestEmails(t, email)
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	token, agentID, _ := testutil.LoginAndGetToken(t, email)

	// Unique markers so the assertion only matches this run's rows.
	runTag := time.Now().UnixNano()
	validItemID := fmt.Sprintf("%d", 9_200_000_000_000_000_000+runTag%1_000_000)
	dedupKey := fmt.Sprintf("ft%013d", runTag%10_000_000_000_000)

	cleanRows := func() {
		testutil.TestDB.Exec("DELETE FROM followup_labels WHERE agent_id = $1", agentID)
	}
	cleanRows()
	t.Cleanup(cleanRows)

	body := map[string]interface{}{
		"events": []map[string]interface{}{
			{
				"item_id":       validItemID,
				"kind":          "surface",
				"brief":         "surfaced in push",
				"server_id":     "srv-test",
				"session_key":   "sess-1",
				"channel":       "lark",
				"dedup_key":     dedupKey,
				"impression_id": "impr-test-1",
				"ts":            time.Now().UnixMilli(),
			},
			{"item_id": validItemID, "kind": "bogus", "dedup_key": dedupKey + "b"}, // invalid kind
			{"item_id": "not-a-number", "kind": "task", "dedup_key": dedupKey + "c"}, // non-numeric item_id
		},
	}

	resp := testutil.DoPost(t, "/api/v1/items/events", body, token)
	if code := int(resp["code"].(float64)); code != 0 {
		t.Fatalf("push events failed: code=%d msg=%v", code, resp["msg"])
	}
	data := resp["data"].(map[string]interface{})
	if accepted := int(data["accepted"].(float64)); accepted != 1 {
		t.Fatalf("accepted = %d, want 1", accepted)
	}
	if skipped := int(data["skipped"].(float64)); skipped != 2 {
		t.Fatalf("skipped = %d, want 2", skipped)
	}

	// Consumer is async; poll the table for the landed row.
	var count int
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := testutil.TestDB.QueryRow(
			"SELECT count(*) FROM followup_labels WHERE dedup_key = $1", dedupKey,
		).Scan(&count); err != nil {
			t.Fatalf("query followup_labels: %v", err)
		}
		if count > 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if count != 1 {
		t.Fatalf("followup_labels rows for dedup_key %s = %d, want 1", dedupKey, count)
	}

	// Verify the persisted row carries the authoritative agent_id and reported fields.
	var gotAgent, gotItem int64
	var gotKind, gotImpr string
	if err := testutil.TestDB.QueryRow(
		"SELECT agent_id, item_id, kind, impression_id FROM followup_labels WHERE dedup_key = $1", dedupKey,
	).Scan(&gotAgent, &gotItem, &gotKind, &gotImpr); err != nil {
		t.Fatalf("scan followup_labels row: %v", err)
	}
	if gotAgent != agentID {
		t.Fatalf("agent_id = %d, want %d (from token)", gotAgent, agentID)
	}
	if gotKind != "surface" || gotImpr != "impr-test-1" {
		t.Fatalf("row mismatch: kind=%s impression_id=%s", gotKind, gotImpr)
	}

	// Re-pushing the same dedup_key is idempotent (ON CONFLICT DO NOTHING).
	resp2 := testutil.DoPost(t, "/api/v1/items/events", body, token)
	if code := int(resp2["code"].(float64)); code != 0 {
		t.Fatalf("second push failed: code=%d", code)
	}
	time.Sleep(2 * time.Second)
	if err := testutil.TestDB.QueryRow(
		"SELECT count(*) FROM followup_labels WHERE dedup_key = $1", dedupKey,
	).Scan(&count); err != nil {
		t.Fatalf("re-query followup_labels: %v", err)
	}
	if count != 1 {
		t.Fatalf("after duplicate push, rows = %d, want 1 (idempotent)", count)
	}

	// Unauthenticated request is rejected.
	unauth := testutil.DoPost(t, "/api/v1/items/events", body, "")
	if code := int(unauth["code"].(float64)); code != 401 {
		t.Fatalf("unauthenticated push code = %d, want 401", code)
	}
}
