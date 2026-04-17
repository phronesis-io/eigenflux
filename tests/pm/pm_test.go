package pm

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/pm"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/tests/testutil"
	"github.com/cloudwego/kitex/client"
)

var pmRPCClient pmservice.Client

func getPMClient(t *testing.T) pmservice.Client {
	t.Helper()
	if pmRPCClient != nil {
		return pmRPCClient
	}
	c, err := pmservice.NewClient("PMService",
		client.WithHostPorts(fmt.Sprintf("127.0.0.1:%d", config.Load().PMRPCPort)))
	if err != nil {
		t.Fatalf("pm rpc client: %v", err)
	}
	pmRPCClient = c
	return c
}

func TestMain(m *testing.M) {
	testutil.RunTestMain(m)
}

// cleanPMData cleans PM-related DB and Redis data for given agent IDs.
func cleanPMData(t *testing.T, agentIDs ...int64) {
	t.Helper()
	ctx := context.Background()
	rdb := testutil.GetTestRedis()

	for _, id := range agentIDs {
		testutil.TestDB.Exec("DELETE FROM private_messages WHERE sender_id = $1 OR receiver_id = $1", id)
		testutil.TestDB.Exec("DELETE FROM conversations WHERE participant_a = $1 OR participant_b = $1", id)
		rdb.Del(ctx, fmt.Sprintf("pm:fetch:%d", id))
	}
	cleanRedisPattern(ctx, "pm:ice:h:*")
	cleanRedisPattern(ctx, "pm:lock:*")
	cleanRedisPattern(ctx, "pm:convmap:*")
	cleanRedisPattern(ctx, "pm:itemowner:*")
	cleanRedisPattern(ctx, "pm:itemresp:*")
	cleanRedisPattern(ctx, "pm:conv:*")
}

func cleanRedisPattern(ctx context.Context, pattern string) {
	rdb := testutil.GetTestRedis()
	keys, _ := rdb.Keys(ctx, pattern).Result()
	if len(keys) > 0 {
		rdb.Del(ctx, keys...)
	}
}

// mockItem inserts a raw_item + processed_item directly in DB, bypassing the LLM pipeline.
func mockItem(t *testing.T, itemID, authorAgentID int64, expectedResponse string) {
	t.Helper()
	now := time.Now().UnixMilli()

	// Delete first to ensure clean state
	testutil.TestDB.Exec("DELETE FROM processed_items WHERE item_id = $1", itemID)
	testutil.TestDB.Exec("DELETE FROM raw_items WHERE item_id = $1", itemID)

	_, err := testutil.TestDB.Exec(
		`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES ($1, $2, $3, $4)`,
		itemID, authorAgentID, "mock content for PM test", now,
	)
	if err != nil {
		t.Fatalf("failed to insert mock raw_item: %v", err)
	}

	var expResp *string
	if expectedResponse != "" {
		expResp = &expectedResponse
	}
	_, err = testutil.TestDB.Exec(
		`INSERT INTO processed_items (item_id, status, broadcast_type, expected_response, updated_at) VALUES ($1, 3, 'info', $2, $3)`,
		itemID, expResp, now,
	)
	if err != nil {
		t.Fatalf("failed to insert mock processed_item: %v", err)
	}
}

func cleanMockItems(t *testing.T, itemIDs ...int64) {
	t.Helper()
	for _, id := range itemIDs {
		testutil.TestDB.Exec("DELETE FROM processed_items WHERE item_id = $1", id)
		testutil.TestDB.Exec("DELETE FROM raw_items WHERE item_id = $1", id)
	}
}

func TestPMFullFlow(t *testing.T) {
	testutil.WaitForAPI(t)

	// Use fixed emails for PM tests
	emails := []string{"pm_author@test.com", "pm_user@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	// Register two agents
	author := testutil.RegisterAgent(t, "pm_author@test.com", "PM Author", "I publish items")
	user := testutil.RegisterAgent(t, "pm_user@test.com", "PM User", "I read items")

	authorID, _ := strconv.ParseInt(author["agent_id"].(string), 10, 64)
	userID, _ := strconv.ParseInt(user["agent_id"].(string), 10, 64)
	authorToken := author["token"].(string)
	userToken := user["token"].(string)

	// Clean stale data from previous runs
	cleanPMData(t, authorID, userID)

	// Mock an item owned by author
	mockItemID := int64(7770001)
	mockItem(t, mockItemID, authorID, "")
	defer cleanMockItems(t, mockItemID)
	defer cleanPMData(t, authorID, userID)

	var convID string

	// ============================================================
	// Test 1: SendPM — new conversation
	// ============================================================
	t.Run("SendPM_NewConversation", func(t *testing.T) {
		resp := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
			"content": "Hello, I saw your item!",
			"item_id": strconv.FormatInt(mockItemID, 10),
		}, userToken)

		code := int(resp["code"].(float64))
		if code != 0 {
			t.Fatalf("SendPM failed: code=%d msg=%v", code, resp["msg"])
		}
		data := resp["data"].(map[string]interface{})
		if _, ok := data["msg_id"].(string); !ok {
			t.Fatalf("expected msg_id as string, got %T", data["msg_id"])
		}
		if _, ok := data["conv_id"].(string); !ok {
			t.Fatalf("expected conv_id as string, got %T", data["conv_id"])
		}
		convID = data["conv_id"].(string)
		t.Logf("SendPM OK: msg_id=%s conv_id=%s", data["msg_id"], data["conv_id"])
	})

	// ============================================================
	// Test 2: SendPM — ice break blocks same sender (via item_id)
	// ============================================================
	t.Run("SendPM_IceBreakBlocksSameSender", func(t *testing.T) {
		resp := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
			"content": "Another message before reply",
			"item_id": strconv.FormatInt(mockItemID, 10),
		}, userToken)

		code := int(resp["code"].(float64))
		if code != 429 {
			t.Fatalf("expected code=429 (ice break), got code=%d msg=%v", code, resp["msg"])
		}
	})

	// ============================================================
	// Test 2b: SendPM — ice break blocks same sender via conv_id
	// ============================================================
	t.Run("SendPM_IceBreakBlocksViaConvID", func(t *testing.T) {
		if convID == "" {
			t.Skip("skipped: no conv_id from previous steps")
		}
		resp := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
			"content": "Trying to continue via conv_id before ice break",
			"conv_id": convID,
		}, userToken)

		code := int(resp["code"].(float64))
		if code != 429 {
			t.Fatalf("expected code=429 (ice break via conv_id), got code=%d msg=%v", code, resp["msg"])
		}
	})

	// ============================================================
	// Test 3: FetchPM — author sees unread message
	// ============================================================
	t.Run("FetchPM_AuthorSeesMessage", func(t *testing.T) {
		resp := testutil.DoGet(t, "/api/v1/pm/fetch", authorToken)
		code := int(resp["code"].(float64))
		if code != 0 {
			t.Fatalf("FetchPM failed: code=%d msg=%v", code, resp["msg"])
		}
		data := resp["data"].(map[string]interface{})
		messages := data["messages"].([]interface{})
		if len(messages) != 1 {
			t.Fatalf("expected 1 unread message, got %d", len(messages))
		}
		msg := messages[0].(map[string]interface{})
		if msg["content"].(string) != "Hello, I saw your item!" {
			t.Fatalf("unexpected message content: %v", msg["content"])
		}
		// Verify agent names are present
		if msg["sender_name"].(string) != "PM User" {
			t.Fatalf("expected sender_name='PM User', got %v", msg["sender_name"])
		}
		if msg["receiver_name"].(string) != "PM Author" {
			t.Fatalf("expected receiver_name='PM Author', got %v", msg["receiver_name"])
		}
		convID = msg["conv_id"].(string)
		t.Logf("FetchPM OK: got message in conv_id=%s", convID)
	})

	// ============================================================
	// Test 4: SendPM — author replies (breaks ice)
	// ============================================================
	t.Run("SendPM_AuthorReplies", func(t *testing.T) {
		resp := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
			"content": "Thanks for reaching out!",
			"conv_id": convID,
		}, authorToken)

		code := int(resp["code"].(float64))
		if code != 0 {
			t.Fatalf("SendPM reply failed: code=%d msg=%v", code, resp["msg"])
		}
	})

	// ============================================================
	// Test 5: SendPM — after ice break, user can send freely
	// ============================================================
	t.Run("SendPM_AfterIceBreakFreely", func(t *testing.T) {
		resp := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
			"content": "Great, let me share more details",
			"conv_id": convID,
		}, userToken)

		code := int(resp["code"].(float64))
		if code != 0 {
			t.Fatalf("SendPM after ice break failed: code=%d msg=%v", code, resp["msg"])
		}
	})

	// ============================================================
	// Test 6: ListConversations — user sees ice-broken conversation
	// ============================================================
	t.Run("ListConversations", func(t *testing.T) {
		resp := testutil.DoGet(t, "/api/v1/pm/conversations", userToken)
		code := int(resp["code"].(float64))
		if code != 0 {
			t.Fatalf("ListConversations failed: code=%d msg=%v", code, resp["msg"])
		}
		data := resp["data"].(map[string]interface{})
		convs := data["conversations"].([]interface{})
		if len(convs) == 0 {
			t.Fatalf("expected at least 1 conversation, got 0")
		}
		conv := convs[0].(map[string]interface{})
		if conv["conv_id"].(string) != convID {
			t.Fatalf("expected conv_id=%s, got %s", convID, conv["conv_id"])
		}
		// Verify participant IDs are present
		if conv["participant_a"] == nil || conv["participant_b"] == nil {
			t.Fatalf("expected participant_a and participant_b in response")
		}
		// Verify participant names are present
		if conv["participant_a_name"] == nil || conv["participant_b_name"] == nil {
			t.Fatalf("expected participant_a_name and participant_b_name in response")
		}
		aName := conv["participant_a_name"].(string)
		bName := conv["participant_b_name"].(string)
		if (aName != "PM Author" && aName != "PM User") || (bName != "PM Author" && bName != "PM User") {
			t.Fatalf("unexpected participant names: a=%s b=%s", aName, bName)
		}
		t.Logf("ListConversations OK: found conv_id=%s a_name=%s b_name=%s", convID, aName, bName)
	})

	// ============================================================
	// Test 7: GetConvHistory — paginated message history
	// ============================================================
	t.Run("GetConvHistory", func(t *testing.T) {
		path := fmt.Sprintf("/api/v1/pm/history?conv_id=%s", convID)
		resp := testutil.DoGet(t, path, userToken)
		code := int(resp["code"].(float64))
		if code != 0 {
			t.Fatalf("GetConvHistory failed: code=%d msg=%v", code, resp["msg"])
		}
		data := resp["data"].(map[string]interface{})
		messages := data["messages"].([]interface{})
		// Should have 3 messages: user→author, author→user, user→author
		if len(messages) != 3 {
			t.Fatalf("expected 3 messages in history, got %d", len(messages))
		}
		// Verify names on messages
		for _, m := range messages {
			msg := m.(map[string]interface{})
			if msg["sender_name"] == nil || msg["receiver_name"] == nil {
				t.Fatalf("expected sender_name and receiver_name on history message")
			}
		}
		t.Logf("GetConvHistory OK: %d messages with names", len(messages))
	})

	// ============================================================
	// Test 8: GetConvHistory — non-participant gets 403
	// ============================================================
	t.Run("GetConvHistory_NonParticipant", func(t *testing.T) {
		// Register a third agent
		third := testutil.RegisterAgent(t, "pm_third@test.com", "Third", "outsider")
		defer testutil.CleanupTestEmails(t, "pm_third@test.com")

		path := fmt.Sprintf("/api/v1/pm/history?conv_id=%s", convID)
		resp := testutil.DoGet(t, path, third["token"].(string))
		code := int(resp["code"].(float64))
		if code != 403 {
			t.Fatalf("expected 403 for non-participant, got code=%d", code)
		}
	})

	// ============================================================
	// Test 9: GetConvHistory — cursor pagination
	// ============================================================
	t.Run("GetConvHistory_Cursor", func(t *testing.T) {
		if convID == "" {
			t.Skip("skipped: no conv_id from previous steps")
		}
		// Fetch with limit=1
		path := fmt.Sprintf("/api/v1/pm/history?conv_id=%s&limit=1", convID)
		resp := testutil.DoGet(t, path, userToken)
		code := int(resp["code"].(float64))
		if code != 0 {
			t.Fatalf("GetConvHistory failed: code=%d", code)
		}
		data := resp["data"].(map[string]interface{})
		messages := data["messages"].([]interface{})
		if len(messages) != 1 {
			t.Fatalf("expected 1 message with limit=1, got %d", len(messages))
		}
		cursor := data["next_cursor"].(string)

		// Fetch next page
		path2 := fmt.Sprintf("/api/v1/pm/history?conv_id=%s&limit=1&cursor=%s", convID, cursor)
		resp2 := testutil.DoGet(t, path2, userToken)
		data2 := resp2["data"].(map[string]interface{})
		messages2 := data2["messages"].([]interface{})
		if len(messages2) != 1 {
			t.Fatalf("expected 1 message on second page, got %d", len(messages2))
		}
		msg1ID := messages[0].(map[string]interface{})["msg_id"].(string)
		msg2ID := messages2[0].(map[string]interface{})["msg_id"].(string)
		if msg1ID == msg2ID {
			t.Fatalf("cursor pagination returned same message")
		}
		t.Logf("GetConvHistory cursor pagination OK")
	})

	// ============================================================
	// Test 10: CloseConv — close the conversation
	// ============================================================
	t.Run("CloseConv", func(t *testing.T) {
		resp := testutil.DoPost(t, "/api/v1/pm/close", map[string]string{
			"conv_id": convID,
		}, authorToken)
		code := int(resp["code"].(float64))
		if code != 0 {
			t.Fatalf("CloseConv failed: code=%d msg=%v", code, resp["msg"])
		}
		t.Logf("CloseConv OK")
	})

	// ============================================================
	// Test 11: SendPM after close should be rejected
	// ============================================================
	t.Run("SendPM_AfterClose", func(t *testing.T) {
		if convID == "" {
			t.Skip("skipped: no conv_id from previous steps")
		}
		resp := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
			"receiver_id": strconv.FormatInt(authorID, 10),
			"content":     "Should be rejected after close",
			"conv_id":     convID,
		}, userToken)
		code := int(resp["code"].(float64))
		if code == 0 {
			t.Fatalf("expected error sending to closed conversation, got success")
		}
		t.Logf("SendPM after close correctly rejected: code=%d msg=%v", code, resp["msg"])
	})

	// ============================================================
	// Test 12: After close, conversation no longer listed
	// ============================================================
	t.Run("ListConversations_AfterClose", func(t *testing.T) {
		resp := testutil.DoGet(t, "/api/v1/pm/conversations", userToken)
		code := int(resp["code"].(float64))
		if code != 0 {
			t.Fatalf("ListConversations failed: code=%d msg=%v", code, resp["msg"])
		}
		data := resp["data"].(map[string]interface{})
		convs := data["conversations"].([]interface{})
		for _, c := range convs {
			conv := c.(map[string]interface{})
			if conv["conv_id"].(string) == convID {
				t.Fatalf("closed conversation should not appear in list")
			}
		}
		t.Logf("ListConversations after close: correctly excluded")
	})
}

func TestSendPM_NoReplyItem(t *testing.T) {
	testutil.WaitForAPI(t)

	emails := []string{"pm_noreply_author@test.com", "pm_noreply_user@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	author := testutil.RegisterAgent(t, "pm_noreply_author@test.com", "NoReply Author", "bio")
	user := testutil.RegisterAgent(t, "pm_noreply_user@test.com", "NoReply User", "bio")

	authorID, _ := strconv.ParseInt(author["agent_id"].(string), 10, 64)
	userID, _ := strconv.ParseInt(user["agent_id"].(string), 10, 64)
	_ = userID

	// Mock item with expected_response=no_reply
	mockItemID := int64(7770002)
	mockItem(t, mockItemID, authorID, "no_reply")
	defer cleanMockItems(t, mockItemID)
	defer cleanPMData(t, authorID, userID)

	resp := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
		"content": "Trying to message no_reply item",
		"item_id": strconv.FormatInt(mockItemID, 10),
	}, user["token"].(string))

	code := int(resp["code"].(float64))
	if code != 403 {
		t.Fatalf("expected code=403 for no_reply item, got code=%d msg=%v", code, resp["msg"])
	}
	t.Logf("SendPM no_reply correctly rejected")
}

func TestSendPM_ItemID_IgnoresReceiverIDValidation(t *testing.T) {
	testutil.WaitForAPI(t)

	emails := []string{"pm_owner_a@test.com", "pm_owner_b@test.com", "pm_owner_c@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "pm_owner_a@test.com", "Agent A", "bio")
	agentB := testutil.RegisterAgent(t, "pm_owner_b@test.com", "Agent B", "bio")
	agentC := testutil.RegisterAgent(t, "pm_owner_c@test.com", "Agent C", "bio")

	agentAID, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	agentBID, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	agentCID, _ := strconv.ParseInt(agentC["agent_id"].(string), 10, 64)

	// Mock item owned by A
	mockItemID := int64(7770003)
	mockItem(t, mockItemID, agentAID, "")
	defer cleanMockItems(t, mockItemID)
	defer cleanPMData(t, agentAID, agentBID, agentCID)

	// B replies by item_id while passing a mismatched receiver_id. The server should
	// ignore the provided receiver_id and route the message to the item owner.
	resp := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
		"receiver_id": strconv.FormatInt(agentCID, 10),
		"content":     "Route to item owner instead",
		"item_id":     strconv.FormatInt(mockItemID, 10),
	}, agentB["token"].(string))

	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("expected success for item-based PM with mismatched receiver_id, got code=%d msg=%v", code, resp["msg"])
	}

	fetchResp := testutil.DoGet(t, "/api/v1/pm/fetch", agentA["token"].(string))
	if int(fetchResp["code"].(float64)) != 0 {
		t.Fatalf("FetchPM failed for item owner: %v", fetchResp["msg"])
	}
	messages := fetchResp["data"].(map[string]interface{})["messages"].([]interface{})
	if len(messages) != 1 {
		t.Fatalf("expected 1 message for item owner, got %d", len(messages))
	}
	msg := messages[0].(map[string]interface{})
	if msg["receiver_id"].(string) != agentA["agent_id"].(string) {
		t.Fatalf("expected receiver_id=%s, got %v", agentA["agent_id"], msg["receiver_id"])
	}
	t.Logf("Item-based PM ignored mismatched receiver_id and delivered to owner")
}

func TestSendPM_Unauthorized(t *testing.T) {
	testutil.WaitForAPI(t)

	resp := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
		"receiver_id": "12345",
		"content":     "no auth",
		"item_id":     "99999",
	}, "")

	code := int(resp["code"].(float64))
	if code != 401 {
		t.Fatalf("expected 401 for unauthorized, got %d", code)
	}
}

func TestCloseConv_NonItemOriginated(t *testing.T) {
	testutil.WaitForAPI(t)

	emails := []string{"pm_close_a@test.com", "pm_close_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "pm_close_a@test.com", "Close A", "bio")
	agentB := testutil.RegisterAgent(t, "pm_close_b@test.com", "Close B", "bio")

	agentAID, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	agentBID, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)

	// Insert a conversation with origin_id=0 directly in DB
	participantA, participantB := agentAID, agentBID
	if participantA > participantB {
		participantA, participantB = participantB, participantA
	}
	now := time.Now().UnixMilli()
	convID := int64(8880001)
	_, err := testutil.TestDB.Exec(
		`INSERT INTO conversations (conv_id, participant_a, participant_b, initiator_id, last_sender_id, origin_type, origin_id, msg_count, status, updated_at)
		 VALUES ($1, $2, $3, $4, $4, '', 0, 2, 0, $5)`,
		convID, participantA, participantB, agentAID, now,
	)
	if err != nil {
		t.Fatalf("failed to insert test conversation: %v", err)
	}
	defer cleanPMData(t, agentAID, agentBID)

	resp := testutil.DoPost(t, "/api/v1/pm/close", map[string]string{
		"conv_id": strconv.FormatInt(convID, 10),
	}, agentA["token"].(string))

	code := int(resp["code"].(float64))
	if code == 0 {
		t.Fatalf("expected error for non-item-originated conversation, got success")
	}
	t.Logf("CloseConv non-item-originated correctly rejected: code=%d", code)
}

func TestFetchPM_EmptyWhenNoUnread(t *testing.T) {
	testutil.WaitForAPI(t)

	emails := []string{"pm_empty@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agent := testutil.RegisterAgent(t, "pm_empty@test.com", "Empty PM", "bio")
	agentID, _ := strconv.ParseInt(agent["agent_id"].(string), 10, 64)
	defer cleanPMData(t, agentID)

	resp := testutil.DoGet(t, "/api/v1/pm/fetch", agent["token"].(string))
	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("FetchPM failed: code=%d", code)
	}
	data := resp["data"].(map[string]interface{})
	messages := data["messages"].([]interface{})
	if len(messages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(messages))
	}
}

func TestSendPM_DifferentItemsIndependentConversations(t *testing.T) {
	testutil.WaitForAPI(t)

	emails := []string{"pm_indep_author@test.com", "pm_indep_user@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	author := testutil.RegisterAgent(t, "pm_indep_author@test.com", "Indep Author", "bio")
	user := testutil.RegisterAgent(t, "pm_indep_user@test.com", "Indep User", "bio")

	authorID, _ := strconv.ParseInt(author["agent_id"].(string), 10, 64)
	userID, _ := strconv.ParseInt(user["agent_id"].(string), 10, 64)

	// Mock two different items owned by author
	itemID1 := int64(7770010)
	itemID2 := int64(7770011)
	mockItem(t, itemID1, authorID, "")
	mockItem(t, itemID2, authorID, "")
	defer cleanMockItems(t, itemID1, itemID2)
	defer cleanPMData(t, authorID, userID)

	// Send PM to item 1
	resp1 := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
		"content": "Message about item 1",
		"item_id": strconv.FormatInt(itemID1, 10),
	}, user["token"].(string))
	if int(resp1["code"].(float64)) != 0 {
		t.Fatalf("SendPM to item1 failed: %v", resp1["msg"])
	}
	convID1 := resp1["data"].(map[string]interface{})["conv_id"].(string)

	// Send PM to item 2 — should create a separate conversation, NOT be blocked by ice break
	resp2 := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
		"content": "Message about item 2",
		"item_id": strconv.FormatInt(itemID2, 10),
	}, user["token"].(string))
	if int(resp2["code"].(float64)) != 0 {
		t.Fatalf("SendPM to item2 failed (should not be blocked by item1 ice break): code=%v msg=%v", resp2["code"], resp2["msg"])
	}
	convID2 := resp2["data"].(map[string]interface{})["conv_id"].(string)

	if convID1 == convID2 {
		t.Fatalf("expected different conv_ids for different items, got same: %s", convID1)
	}
	t.Logf("Independent conversations OK: item1→conv=%s, item2→conv=%s", convID1, convID2)
}

func TestFetchPM_CacheInvalidation(t *testing.T) {
	testutil.WaitForAPI(t)

	emails := []string{"pm_cache_author@test.com", "pm_cache_user@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	author := testutil.RegisterAgent(t, "pm_cache_author@test.com", "Cache Author", "bio")
	user := testutil.RegisterAgent(t, "pm_cache_user@test.com", "Cache User", "bio")

	authorID, _ := strconv.ParseInt(author["agent_id"].(string), 10, 64)
	userID, _ := strconv.ParseInt(user["agent_id"].(string), 10, 64)

	cleanPMData(t, authorID, userID)

	mockItemID := int64(7770020)
	mockItem(t, mockItemID, authorID, "")
	defer cleanMockItems(t, mockItemID)
	defer cleanPMData(t, authorID, userID)

	// Step 1: FetchPM should return empty (no messages yet)
	resp := testutil.DoGet(t, "/api/v1/pm/fetch", author["token"].(string))
	if int(resp["code"].(float64)) != 0 {
		t.Fatalf("FetchPM failed: %v", resp["msg"])
	}
	data := resp["data"].(map[string]interface{})
	if len(data["messages"].([]interface{})) != 0 {
		t.Fatalf("expected 0 messages before send")
	}

	// Step 2: Send a PM — this should invalidate the author's fetch cache
	sendResp := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
		"content": "Cache invalidation test",
		"item_id": strconv.FormatInt(mockItemID, 10),
	}, user["token"].(string))
	if int(sendResp["code"].(float64)) != 0 {
		t.Fatalf("SendPM failed: %v", sendResp["msg"])
	}

	// Step 3: FetchPM should now return the new message (cache was invalidated by send)
	resp2 := testutil.DoGet(t, "/api/v1/pm/fetch", author["token"].(string))
	if int(resp2["code"].(float64)) != 0 {
		t.Fatalf("FetchPM after send failed: %v", resp2["msg"])
	}
	data2 := resp2["data"].(map[string]interface{})
	messages := data2["messages"].([]interface{})
	if len(messages) != 1 {
		t.Fatalf("expected 1 message after send (cache invalidated), got %d", len(messages))
	}
	msg := messages[0].(map[string]interface{})
	if msg["content"].(string) != "Cache invalidation test" {
		t.Fatalf("unexpected content: %v", msg["content"])
	}

	// Step 4: FetchPM again immediately — messages already read, should return empty
	// (either from cache or from DB)
	resp3 := testutil.DoGet(t, "/api/v1/pm/fetch", author["token"].(string))
	if int(resp3["code"].(float64)) != 0 {
		t.Fatalf("FetchPM third call failed: %v", resp3["msg"])
	}
	data3 := resp3["data"].(map[string]interface{})
	if len(data3["messages"].([]interface{})) != 0 {
		t.Fatalf("expected 0 messages on third fetch (already read)")
	}

	t.Logf("FetchPM cache invalidation OK")
}

func TestFetchPM_RedisCacheEvictionFallback(t *testing.T) {
	testutil.WaitForAPI(t)

	emails := []string{"pm_evict_author@test.com", "pm_evict_user@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	author := testutil.RegisterAgent(t, "pm_evict_author@test.com", "Evict Author", "bio")
	user := testutil.RegisterAgent(t, "pm_evict_user@test.com", "Evict User", "bio")

	authorID, _ := strconv.ParseInt(author["agent_id"].(string), 10, 64)
	userID, _ := strconv.ParseInt(user["agent_id"].(string), 10, 64)

	cleanPMData(t, authorID, userID)

	mockItemID := int64(7770021)
	mockItem(t, mockItemID, authorID, "")
	defer cleanMockItems(t, mockItemID)
	defer cleanPMData(t, authorID, userID)

	// Send a PM
	sendResp := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
		"content": "Redis eviction test",
		"item_id": strconv.FormatInt(mockItemID, 10),
	}, user["token"].(string))
	if int(sendResp["code"].(float64)) != 0 {
		t.Fatalf("SendPM failed: %v", sendResp["msg"])
	}

	// Manually delete the fetch cache key to simulate Redis eviction
	ctx := context.Background()
	rdb := testutil.GetTestRedis()
	rdb.Del(ctx, fmt.Sprintf("pm:fetch:%d", authorID))

	// FetchPM should still return the message by falling through to DB
	resp := testutil.DoGet(t, "/api/v1/pm/fetch", author["token"].(string))
	if int(resp["code"].(float64)) != 0 {
		t.Fatalf("FetchPM after cache eviction failed: %v", resp["msg"])
	}
	data := resp["data"].(map[string]interface{})
	messages := data["messages"].([]interface{})
	if len(messages) != 1 {
		t.Fatalf("expected 1 message after cache eviction (DB fallback), got %d", len(messages))
	}

	t.Logf("FetchPM Redis eviction fallback OK")
}

func TestFetchPMHistory_SelectionAndOrdering(t *testing.T) {
	testutil.WaitForAPI(t)

	emails := []string{"pm_hist_author@test.com", "pm_hist_user@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	author := testutil.RegisterAgent(t, "pm_hist_author@test.com", "Hist Author", "bio")
	user := testutil.RegisterAgent(t, "pm_hist_user@test.com", "Hist User", "bio")

	authorID, _ := strconv.ParseInt(author["agent_id"].(string), 10, 64)
	userID, _ := strconv.ParseInt(user["agent_id"].(string), 10, 64)

	itemID := int64(7780100)
	mockItem(t, itemID, authorID, "")
	defer cleanMockItems(t, itemID)
	defer cleanPMData(t, authorID, userID)

	r1 := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
		"content": "msg A (user→author)",
		"item_id": strconv.FormatInt(itemID, 10),
	}, user["token"].(string))
	if int(r1["code"].(float64)) != 0 {
		t.Fatalf("send #1 failed: %v", r1["msg"])
	}

	fetchResp := testutil.DoGet(t, "/api/v1/pm/fetch", author["token"].(string))
	if int(fetchResp["code"].(float64)) != 0 {
		t.Fatalf("fetch(author) failed")
	}

	convID := r1["data"].(map[string]interface{})["conv_id"].(string)

	r2 := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
		"content": "msg B (author→user)",
		"conv_id": convID,
	}, author["token"].(string))
	if int(r2["code"].(float64)) != 0 {
		t.Fatalf("send #2 failed: %v", r2["msg"])
	}

	r3 := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
		"content": "msg C (user→author, unread)",
		"conv_id": convID,
	}, user["token"].(string))
	if int(r3["code"].(float64)) != 0 {
		t.Fatalf("send #3 failed: %v", r3["msg"])
	}

	c := getPMClient(t)
	hist, err := c.FetchPMHistory(context.Background(), &pm.FetchPMHistoryReq{AgentId: authorID})
	if err != nil {
		t.Fatalf("FetchPMHistory RPC: %v", err)
	}
	if hist.BaseResp.Code != 0 {
		t.Fatalf("FetchPMHistory code=%d msg=%s", hist.BaseResp.Code, hist.BaseResp.Msg)
	}

	got := make(map[string]bool)
	for _, m := range hist.Messages {
		got[m.Content] = true
	}
	if !got["msg A (user→author)"] {
		t.Errorf("expected msg A in history, got: %v", got)
	}
	if !got["msg B (author→user)"] {
		t.Errorf("expected msg B in history, got: %v", got)
	}
	if got["msg C (user→author, unread)"] {
		t.Errorf("unread msg C should NOT appear in history")
	}

	for i := 1; i < len(hist.Messages); i++ {
		if hist.Messages[i-1].MsgId < hist.Messages[i].MsgId {
			t.Errorf("messages not in msg_id DESC order at index %d", i)
		}
	}
}

func TestFetchPMHistory_LimitClamp(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"pm_hist_limit@test.com"}
	testutil.CleanupTestEmails(t, emails...)
	agent := testutil.RegisterAgent(t, "pm_hist_limit@test.com", "Limit", "bio")
	agentID, _ := strconv.ParseInt(agent["agent_id"].(string), 10, 64)
	defer cleanPMData(t, agentID)

	c := getPMClient(t)

	limit := int32(200)
	r, err := c.FetchPMHistory(context.Background(), &pm.FetchPMHistoryReq{AgentId: agentID, Limit: &limit})
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if r.BaseResp.Code != 0 {
		t.Fatalf("unexpected code=%d msg=%s", r.BaseResp.Code, r.BaseResp.Msg)
	}
	if len(r.Messages) > 50 {
		t.Errorf("limit not clamped: got %d messages", len(r.Messages))
	}

	zero := int32(0)
	r, err = c.FetchPMHistory(context.Background(), &pm.FetchPMHistoryReq{AgentId: agentID, Limit: &zero})
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if r.BaseResp.Code != 0 {
		t.Fatalf("unexpected code=%d msg=%s", r.BaseResp.Code, r.BaseResp.Msg)
	}
	if len(r.Messages) > 50 {
		t.Errorf("limit not clamped: got %d", len(r.Messages))
	}
}

func TestFetchPMHistory_Empty(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"pm_hist_empty@test.com"}
	testutil.CleanupTestEmails(t, emails...)
	agent := testutil.RegisterAgent(t, "pm_hist_empty@test.com", "Empty", "bio")
	agentID, _ := strconv.ParseInt(agent["agent_id"].(string), 10, 64)
	defer cleanPMData(t, agentID)

	c := getPMClient(t)
	r, err := c.FetchPMHistory(context.Background(), &pm.FetchPMHistoryReq{AgentId: agentID})
	if err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if r.BaseResp.Code != 0 {
		t.Fatalf("code=%d msg=%s", r.BaseResp.Code, r.BaseResp.Msg)
	}
	if len(r.Messages) != 0 {
		t.Errorf("expected 0 messages for new agent, got %d", len(r.Messages))
	}
}

