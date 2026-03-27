package e2e_test

import (
	"fmt"
	"testing"

	"eigenflux_server/tests/testutil"
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
