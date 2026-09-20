package pm

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"eigenflux_server/tests/testutil"
)

// The V2 routes (/api/v2/relations/friend-requests, /api/v2/relations/friends/block,
// /api/v2/pm/messages) bind the same gateway handlers and PM RPC as the V1
// paths exercised here; the self-target guard lives in the PM RPC.

func assertCode400(t *testing.T, resp map[string]interface{}, what string) {
	t.Helper()
	if code := int(resp["code"].(float64)); code != 400 {
		t.Fatalf("%s: expected code 400, got code=%d msg=%v data=%v", what, code, resp["msg"], resp["data"])
	}
}

func countRows(t *testing.T, query string, args ...interface{}) int64 {
	t.Helper()
	var count int64
	if err := testutil.TestDB.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("query %q failed: %v", query, err)
	}
	return count
}

func TestSendFriendRequest_SelfTargetRejected(t *testing.T) {
	testutil.WaitForAPI(t)
	email := "self_apply@test.com"
	testutil.CleanupTestEmails(t, email)
	// The legacy to_email selector caches email→agent_id for 24h; drop any
	// entry left by a previous run so it resolves the agent registered below.
	testutil.GetTestRedis().Del(context.Background(), "cache:email2uid:"+email)

	agent := testutil.RegisterAgent(t, email, "Self Apply", "bio")
	uid, _ := strconv.ParseInt(agent["agent_id"].(string), 10, 64)
	token := agent["token"].(string)
	rateLimitKey := fmt.Sprintf("ratelimit:friend_request:%d", uid)
	defer cleanRelationsData(t, uid)
	defer testutil.GetTestRedis().Del(context.Background(), rateLimitKey)

	var shortID string
	if err := testutil.TestDB.QueryRow("SELECT COALESCE(short_id, '') FROM agents WHERE agent_id = $1", uid).Scan(&shortID); err != nil || shortID == "" {
		t.Fatalf("failed to load short_id for agent %d: %v", uid, err)
	}

	selectors := []struct {
		name string
		body map[string]string
	}{
		{name: "to_uid", body: map[string]string{"to_uid": agent["agent_id"].(string), "greeting": "hello me"}},
		{name: "to_short_id", body: map[string]string{"to_short_id": shortID, "greeting": "hello me"}},
		{name: "to_email", body: map[string]string{"to_email": email, "greeting": "hello me"}},
	}
	for _, tc := range selectors {
		t.Run(tc.name, func(t *testing.T) {
			resp := testutil.DoPost(t, "/api/v1/relations/apply", tc.body, token)
			assertCode400(t, resp, "self-targeted friend request")
			if count := countRows(t, "SELECT COUNT(*) FROM friend_requests WHERE from_uid = $1 AND to_uid = $1", uid); count != 0 {
				t.Fatalf("expected no self-targeted friend_requests row, got %d", count)
			}
		})
	}

	// A rejected self-request must not consume the hourly friend-request budget.
	exists, err := testutil.GetTestRedis().Exists(context.Background(), rateLimitKey).Result()
	if err != nil {
		t.Fatalf("failed to read rate-limit key: %v", err)
	}
	if exists != 0 {
		t.Fatalf("expected rate-limit key %s to be untouched by self-targeted requests", rateLimitKey)
	}

	listResp := testutil.DoGet(t, "/api/v1/relations/applications?direction=outgoing", token)
	if int(listResp["code"].(float64)) != 0 {
		t.Fatalf("ListFriendRequests failed: %v", listResp["msg"])
	}
	if requests, _ := listResp["data"].(map[string]interface{})["requests"].([]interface{}); len(requests) != 0 {
		t.Fatalf("expected no outgoing requests after self-targeted attempts, got %d", len(requests))
	}
}

func TestBlockUser_SelfTargetRejected(t *testing.T) {
	testutil.WaitForAPI(t)
	email := "self_block@test.com"
	testutil.CleanupTestEmails(t, email)

	agent := testutil.RegisterAgent(t, email, "Self Block", "bio")
	uid, _ := strconv.ParseInt(agent["agent_id"].(string), 10, 64)
	token := agent["token"].(string)
	defer cleanRelationsData(t, uid)

	resp := testutil.DoPost(t, "/api/v1/relations/block", map[string]string{
		"to_uid": agent["agent_id"].(string),
		"remark": "me",
	}, token)
	assertCode400(t, resp, "self-targeted block")

	if count := countRows(t, "SELECT COUNT(*) FROM user_relations WHERE from_uid = $1 AND to_uid = $1", uid); count != 0 {
		t.Fatalf("expected no self-targeted user_relations row, got %d", count)
	}
	blocked, err := testutil.GetTestRedis().SIsMember(context.Background(), fmt.Sprintf("block:%d", uid), uid).Result()
	if err != nil {
		t.Fatalf("failed to read block cache: %v", err)
	}
	if blocked {
		t.Fatalf("expected block cache not to contain the agent itself")
	}
}

func TestSendPM_SelfTargetRejected(t *testing.T) {
	testutil.WaitForAPI(t)
	email := "self_pm@test.com"
	testutil.CleanupTestEmails(t, email)

	agent := testutil.RegisterAgent(t, email, "Self PM", "bio")
	uid, _ := strconv.ParseInt(agent["agent_id"].(string), 10, 64)
	token := agent["token"].(string)

	mockItemID := int64(7790301)
	mockItem(t, mockItemID, uid, "")
	defer cleanMockItems(t, mockItemID)
	defer cleanPMData(t, uid)

	assertNoSelfConversation := func(t *testing.T) {
		t.Helper()
		if count := countRows(t, "SELECT COUNT(*) FROM conversations WHERE participant_a = $1 AND participant_b = $1", uid); count != 0 {
			t.Fatalf("expected no self conversation, got %d", count)
		}
		if count := countRows(t, "SELECT COUNT(*) FROM private_messages WHERE sender_id = $1 AND receiver_id = $1", uid); count != 0 {
			t.Fatalf("expected no self message, got %d", count)
		}
	}

	t.Run("own broadcast item", func(t *testing.T) {
		resp := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
			"content": "Replying to my own broadcast",
			"item_id": strconv.FormatInt(mockItemID, 10),
		}, token)
		assertCode400(t, resp, "self-targeted item message")
		assertNoSelfConversation(t)
	})

	t.Run("friend receiver_id", func(t *testing.T) {
		resp := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
			"content":     "Direct message to myself",
			"receiver_id": agent["agent_id"].(string),
		}, token)
		assertCode400(t, resp, "self-targeted friend message")
		assertNoSelfConversation(t)
	})
}
