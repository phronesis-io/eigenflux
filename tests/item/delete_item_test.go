package item_test

import (
	"fmt"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/rpc/item/dal"
	"eigenflux_server/tests/testutil"

	"gorm.io/gorm/logger"
)

func TestDeleteMyItem(t *testing.T) {
	testutil.WaitForAPI(t)
	testutil.CleanTestData(t)

	agent := testutil.RegisterAgent(t, "delete@test.com", "DeleteBot", "Test delete")
	token := agent["token"].(string)

	itemResp := testutil.PublishItem(t, token, "Test content to delete", `{"type":"info","domains":["tech"],"summary":"test","expire_time":"2026-12-31T00:00:00Z","source_type":"original"}`, "")
	itemID := testutil.MustID(t, itemResp["item_id"], "item_id")

	deleteResp := testutil.DoDelete(t, fmt.Sprintf("/api/v1/agents/items/%d", itemID), token)
	if code := deleteResp["code"].(float64); code != 0 {
		t.Fatalf("delete failed: code=%.0f, msg=%s", code, deleteResp["msg"])
	}

	myItems := testutil.GetMyItems(t, token, 0, 20)
	items := myItems["items"].([]interface{})
	for _, item := range items {
		if testutil.MustID(t, item.(map[string]interface{})["item_id"], "item_id") == itemID {
			t.Fatal("deleted item still in my items")
		}
	}
}

func TestDeleteItemUnauthorized(t *testing.T) {
	testutil.WaitForAPI(t)
	testutil.CleanTestData(t)

	agent1 := testutil.RegisterAgent(t, "owner@test.com", "Owner", "Owner")
	agent2 := testutil.RegisterAgent(t, "other@test.com", "Other", "Other")
	token1 := agent1["token"].(string)
	token2 := agent2["token"].(string)

	itemResp := testutil.PublishItem(t, token1, "Owner's item", `{"type":"info","domains":["tech"],"summary":"test","expire_time":"2026-12-31T00:00:00Z","source_type":"original"}`, "")
	itemID := testutil.MustID(t, itemResp["item_id"], "item_id")

	deleteResp := testutil.DoDelete(t, fmt.Sprintf("/api/v1/agents/items/%d", itemID), token2)
	if code := deleteResp["code"].(float64); code != 403 {
		t.Fatalf("expected 403, got %.0f", code)
	}
}

// TestDeleteItemRaceCondition tests that deletion is terminal
// and cannot be overwritten by async pipeline updates
func TestDeleteItemRaceCondition(t *testing.T) {
	testutil.WaitForAPI(t)
	testutil.CleanTestData(t)

	cfg := config.Load()
	db.InitWithLogLevel(cfg.PgDSN, logger.Silent)

	agent := testutil.RegisterAgent(t, "race@test.com", "RaceBot", "Test race")
	token := agent["token"].(string)

	itemResp := testutil.PublishItem(t, token, "Test content for race condition", `{"type":"info","domains":["tech"],"summary":"test"}`, "")
	itemID := testutil.MustID(t, itemResp["item_id"], "item_id")

	// Wait for item to be created in DB
	time.Sleep(100 * time.Millisecond)

	// Simulate: item is being processed
	err := dal.UpdateProcessedItemStatus(db.DB, itemID, dal.StatusProcessing)
	if err != nil {
		t.Fatalf("failed to set status=1: %v", err)
	}

	// User deletes the item (status=5)
	deleteResp := testutil.DoDelete(t, fmt.Sprintf("/api/v1/agents/items/%d", itemID), token)
	if code := deleteResp["code"].(float64); code != 0 {
		t.Fatalf("delete failed: code=%.0f, msg=%s", code, deleteResp["msg"])
	}

	// Verify item is deleted
	item, err := dal.GetProcessedItemByID(db.DB, itemID)
	if err != nil {
		t.Fatalf("failed to get item: %v", err)
	}
	if item.Status != dal.StatusDeleted {
		t.Fatalf("expected status=%d (deleted), got %d", dal.StatusDeleted, item.Status)
	}

	// Simulate: async pipeline tries to update the item to completed
	// This should be IGNORED because deleted is terminal
	err = dal.UpdateProcessedItem(
		db.DB,
		itemID,
		"Summary from LLM",
		"announcement",
		"tech",
		[]string{"test"},
		"",
		"",
		"",
		"reply",
		itemID,
		0.8,
		"en",
		"timely",
		"",
		dal.StatusCompleted,
	)
	if err != nil {
		t.Fatalf("UpdateProcessedItem failed: %v", err)
	}

	// Verify item is STILL deleted
	item, err = dal.GetProcessedItemByID(db.DB, itemID)
	if err != nil {
		t.Fatalf("failed to get item after update: %v", err)
	}
	if item.Status != dal.StatusDeleted {
		t.Fatalf("item should remain deleted, got status=%d", item.Status)
	}
	if item.Summary != "" {
		t.Fatalf("summary should not be updated, got: %s", item.Summary)
	}

	// Also test UpdateProcessedItemStatus
	err = dal.UpdateProcessedItemStatus(db.DB, itemID, dal.StatusCompleted)
	if err != nil {
		t.Fatalf("UpdateProcessedItemStatus failed: %v", err)
	}

	// Verify item is STILL deleted
	item, err = dal.GetProcessedItemByID(db.DB, itemID)
	if err != nil {
		t.Fatalf("failed to get item after status update: %v", err)
	}
	if item.Status != dal.StatusDeleted {
		t.Fatalf("item should remain deleted after status update, got status=%d", item.Status)
	}
}
