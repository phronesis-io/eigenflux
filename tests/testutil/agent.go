package testutil

import (
	"fmt"
	"strconv"
	"testing"
	"time"
)

// RegisterAgent uses the email auth flow to create/login an agent and
// optionally complete the profile. Returns map with "token" and "agent_id".
func RegisterAgent(t *testing.T, email, agentName, bio string) map[string]interface{} {
	t.Helper()
	token, agentID, _ := LoginAndGetToken(t, email)

	if agentName != "" || bio != "" {
		updateBody := map[string]string{}
		if agentName != "" {
			updateBody["agent_name"] = agentName
		}
		if bio != "" {
			updateBody["bio"] = bio
		}
		resp := DoPut(t, "/api/v1/agents/profile", updateBody, token)
		if int(resp["code"].(float64)) != 0 {
			t.Fatalf("profile update failed: %v", resp["msg"])
		}
	}

	return map[string]interface{}{
		"token":    token,
		"agent_id": strconv.FormatInt(agentID, 10),
	}
}

func UpdateProfile(t *testing.T, token, bio string) {
	t.Helper()
	body := map[string]string{"bio": bio}
	resp := DoPut(t, "/api/v1/agents/profile", body, token)
	if int(resp["code"].(float64)) != 0 {
		t.Fatalf("update profile failed: %v", resp["msg"])
	}
}

func GetAgent(t *testing.T, token string) map[string]interface{} {
	t.Helper()
	resp := DoGet(t, "/api/v1/agents/me", token)
	if int(resp["code"].(float64)) != 0 {
		t.Fatalf("get agent failed: %v", resp["msg"])
	}
	data := resp["data"].(map[string]interface{})
	return data
}

func PublishItem(t *testing.T, token, content, notes, url string) map[string]interface{} {
	t.Helper()
	body := map[string]string{"content": content, "notes": notes, "url": url}
	resp := DoPost(t, "/api/v1/items/publish", body, token)
	if int(resp["code"].(float64)) != 0 {
		t.Fatalf("publish failed: %v", resp["msg"])
	}
	data := resp["data"].(map[string]interface{})
	if _, ok := data["item_id"].(string); !ok {
		t.Fatalf("expected publish data.item_id as string, got %T", data["item_id"])
	}
	return data
}

func FetchFeed(t *testing.T, token string, action string, limit int) map[string]interface{} {
	t.Helper()
	if action == "" {
		action = "refresh"
	}
	path := fmt.Sprintf("/api/v1/items/feed?action=%s&limit=%d", action, limit)
	resp := DoGet(t, path, token)
	if int(resp["code"].(float64)) != 0 {
		t.Fatalf("fetch feed failed: %v", resp["msg"])
	}
	data := resp["data"].(map[string]interface{})
	if items, ok := data["items"].([]interface{}); ok {
		for _, it := range items {
			item := it.(map[string]interface{})
			if _, ok := item["item_id"].(string); !ok {
				t.Fatalf("expected feed item_id as string, got %T", item["item_id"])
			}
		}
	}
	return data
}

// FetchFeedRefresh is a convenience wrapper for refresh action
func FetchFeedRefresh(t *testing.T, token string, limit int) map[string]interface{} {
	t.Helper()
	return FetchFeed(t, token, "refresh", limit)
}

// FetchFeedLoadMore is a convenience wrapper for load_more action
func FetchFeedLoadMore(t *testing.T, token string, limit int) map[string]interface{} {
	t.Helper()
	return FetchFeed(t, token, "load_more", limit)
}

func WaitForFeedMinItems(t *testing.T, token string, minItems int, timeout time.Duration) []interface{} {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		RefreshES(t) // ensure newly indexed docs are searchable
		feed := FetchFeedRefresh(t, token, 50)
		items := feed["items"].([]interface{})
		if len(items) >= minItems {
			return items
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for feed items >= %d", minItems)
	return nil
}

func GetItem(t *testing.T, token string, itemID int64) map[string]interface{} {
	t.Helper()
	path := fmt.Sprintf("/api/v1/items/%d", itemID)
	resp := DoGet(t, path, token)
	if int(resp["code"].(float64)) != 0 {
		t.Fatalf("get item failed: %v", resp["msg"])
	}
	data := resp["data"].(map[string]interface{})
	item := data["item"].(map[string]interface{})
	if _, ok := item["item_id"].(string); !ok {
		t.Fatalf("expected item detail item_id as string, got %T", item["item_id"])
	}
	return item
}

func WaitForProfileProcessed(t *testing.T, agentID int64) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var status int
		err := TestDB.QueryRow("SELECT status FROM agent_profiles WHERE agent_id = $1", agentID).Scan(&status)
		if err == nil && status == 3 {
			t.Logf("Profile processing completed for agent %d", agentID)
			return
		}
		if err == nil && status == 2 {
			t.Fatalf("Profile processing failed (status=2) for agent %d", agentID)
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("Timed out waiting for profile processing (agent_id=%d)", agentID)
}

func WaitForItemsProcessed(t *testing.T, itemIDs []int64) {
	t.Helper()
	timeout := 90*time.Second + time.Duration(len(itemIDs))*20*time.Second
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allDone := true
		for _, id := range itemIDs {
			var status int
			err := TestDB.QueryRow("SELECT status FROM processed_items WHERE item_id = $1", id).Scan(&status)
			if err != nil || status != 3 {
				if err == nil && status == 2 {
					t.Fatalf("Item processing failed (status=2) for item %d", id)
				}
				if err == nil && status == 4 {
					t.Fatalf("Item discarded by LLM (status=4) for item %d", id)
				}
				allDone = false
				break
			}
		}
		if allDone {
			t.Logf("All %d items processed", len(itemIDs))
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("Timed out waiting for items processing (items=%d, timeout=%s)", len(itemIDs), timeout)
}

// WaitForItemStatus polls processed_items until the item reaches the expected
// terminal status. Fatals if a different terminal status (2,3,4,5) is reached
// or on timeout.
func WaitForItemStatus(t *testing.T, itemID int64, wantStatus int, timeout time.Duration) {
	t.Helper()
	terminal := map[int]bool{2: true, 3: true, 4: true, 5: true}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var status int
		err := TestDB.QueryRow("SELECT status FROM processed_items WHERE item_id = $1", itemID).Scan(&status)
		if err == nil {
			if status == wantStatus {
				t.Logf("Item %d reached expected status %d", itemID, wantStatus)
				return
			}
			if terminal[status] {
				t.Fatalf("Item %d reached terminal status %d, expected %d", itemID, status, wantStatus)
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("Timed out waiting for item %d to reach status %d (timeout=%s)", itemID, wantStatus, timeout)
}

func GetMyItems(t *testing.T, token string, lastItemID int64, limit int) map[string]interface{} {
	t.Helper()
	path := fmt.Sprintf("/api/v1/agents/items?last_item_id=%d&limit=%d", lastItemID, limit)
	resp := DoGet(t, path, token)
	if int(resp["code"].(float64)) != 0 {
		t.Fatalf("get my items failed: %v", resp["msg"])
	}
	return resp["data"].(map[string]interface{})
}
