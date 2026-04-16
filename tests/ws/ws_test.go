package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"eigenflux_server/pkg/config"
	"eigenflux_server/tests/testutil"
)

func TestMain(m *testing.M) {
	testutil.RunTestMain(m)
}

func wsURL() string {
	cfg := config.Load()
	return fmt.Sprintf("ws://localhost:%d", cfg.WSPort)
}

func dialWS(t *testing.T, token string, cursor int64) *websocket.Conn {
	t.Helper()
	u, _ := url.Parse(wsURL() + "/ws/pm")
	q := u.Query()
	q.Set("token", token)
	if cursor > 0 {
		q.Set("cursor", strconv.FormatInt(cursor, 10))
	}
	u.RawQuery = q.Encode()

	conn, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("ws dial failed: %v (resp=%v)", err, resp)
	}
	return conn
}

func readPush(t *testing.T, conn *websocket.Conn, timeout time.Duration) map[string]interface{} {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ws read failed: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(msg, &result); err != nil {
		t.Fatalf("ws parse failed: %v, body: %s", err, string(msg))
	}
	return result
}

func waitForWS(t *testing.T) {
	t.Helper()
	cfg := config.Load()
	addr := fmt.Sprintf("localhost:%d", cfg.WSPort)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		u := fmt.Sprintf("ws://%s/ws/pm?token=probe", addr)
		conn, _, err := websocket.DefaultDialer.Dial(u, nil)
		if conn != nil {
			conn.Close()
			return
		}
		// If we got an HTTP error response (401 etc), the server IS up
		if err != nil && !strings.Contains(err.Error(), "connection refused") {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("WS service not ready after 30s")
}

func cleanPMData(t *testing.T, agentIDs ...int64) {
	t.Helper()
	ctx := context.Background()
	rdb := testutil.GetTestRedis()
	for _, id := range agentIDs {
		testutil.TestDB.Exec("DELETE FROM private_messages WHERE sender_id = $1 OR receiver_id = $1", id)
		testutil.TestDB.Exec("DELETE FROM conversations WHERE participant_a = $1 OR participant_b = $1", id)
		rdb.Del(ctx, fmt.Sprintf("pm:fetch:%d", id))
	}
}

func mockItem(t *testing.T, itemID, authorAgentID int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	testutil.TestDB.Exec("DELETE FROM processed_items WHERE item_id = $1", itemID)
	testutil.TestDB.Exec("DELETE FROM raw_items WHERE item_id = $1", itemID)
	testutil.TestDB.Exec(
		`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES ($1, $2, $3, $4)`,
		itemID, authorAgentID, "ws test item", now,
	)
	testutil.TestDB.Exec(
		`INSERT INTO processed_items (item_id, status, broadcast_type, updated_at) VALUES ($1, 3, 'info', $2)`,
		itemID, now,
	)
	// Invalidate the PM item-owner Redis cache so GetItemOwner reads fresh DB data.
	rdb := testutil.GetTestRedis()
	rdb.Del(context.Background(), fmt.Sprintf("pm:itemowner:%d", itemID))
}

func cleanMockItem(t *testing.T, itemID int64) {
	t.Helper()
	testutil.TestDB.Exec("DELETE FROM processed_items WHERE item_id = $1", itemID)
	testutil.TestDB.Exec("DELETE FROM raw_items WHERE item_id = $1", itemID)
	rdb := testutil.GetTestRedis()
	rdb.Del(context.Background(), fmt.Sprintf("pm:itemowner:%d", itemID))
}

// --- Test Cases ---

func TestWSAuthFailure(t *testing.T) {
	waitForWS(t)

	u := wsURL() + "/ws/pm?token=invalid_token"
	conn, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if conn != nil {
		conn.Close()
		t.Fatal("expected dial to fail with invalid token")
	}
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
	if resp != nil && resp.StatusCode != 401 {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestWSInitialPush(t *testing.T) {
	testutil.WaitForAPI(t)
	waitForWS(t)

	emails := []string{"ws_sender@test.com", "ws_receiver@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	sender := testutil.RegisterAgent(t, "ws_sender@test.com", "WS Sender", "sends messages")
	receiver := testutil.RegisterAgent(t, "ws_receiver@test.com", "WS Receiver", "receives messages")

	senderID, _ := strconv.ParseInt(sender["agent_id"].(string), 10, 64)
	receiverID, _ := strconv.ParseInt(receiver["agent_id"].(string), 10, 64)
	senderToken := sender["token"].(string)
	receiverToken := receiver["token"].(string)

	cleanPMData(t, senderID, receiverID)

	itemID := int64(990001)
	mockItem(t, itemID, receiverID) // item authored by receiver, so PM goes TO receiver
	defer cleanMockItem(t, itemID)

	// Sender sends PM about receiver's item → PM receiver = item owner = receiverID.
	sendResp := testutil.DoPost(t, "/api/v1/pm/send", map[string]interface{}{
		"receiver_id": receiver["agent_id"],
		"item_id":     strconv.FormatInt(itemID, 10),
		"content":     "hello from ws test",
	}, senderToken)
	if int(sendResp["code"].(float64)) != 0 {
		t.Fatalf("send PM failed: %v", sendResp["msg"])
	}

	// Connect WS with cursor=0, should receive the message immediately.
	ws := dialWS(t, receiverToken, 0)
	defer ws.Close()

	msg := readPush(t, ws, 10*time.Second)
	if msg["type"] != "pm_push" {
		t.Fatalf("expected type pm_push, got %v", msg["type"])
	}
	data := msg["data"].(map[string]interface{})
	messages := data["messages"].([]interface{})
	if len(messages) == 0 {
		t.Fatal("expected at least 1 message in initial push")
	}
	first := messages[0].(map[string]interface{})
	if first["content"] != "hello from ws test" {
		t.Fatalf("expected content 'hello from ws test', got %v", first["content"])
	}

	cleanPMData(t, senderID, receiverID)
}

func TestWSRealtimePush(t *testing.T) {
	testutil.WaitForAPI(t)
	waitForWS(t)

	emails := []string{"ws_rt_sender@test.com", "ws_rt_receiver@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	sender := testutil.RegisterAgent(t, "ws_rt_sender@test.com", "RT Sender", "sends messages")
	receiver := testutil.RegisterAgent(t, "ws_rt_receiver@test.com", "RT Receiver", "receives messages")

	senderID, _ := strconv.ParseInt(sender["agent_id"].(string), 10, 64)
	receiverID, _ := strconv.ParseInt(receiver["agent_id"].(string), 10, 64)
	senderToken := sender["token"].(string)
	receiverToken := receiver["token"].(string)

	cleanPMData(t, senderID, receiverID)

	// Connect WS first (no pending messages).
	ws := dialWS(t, receiverToken, 0)
	defer ws.Close()

	// Brief wait for subscription to settle.
	time.Sleep(500 * time.Millisecond)

	itemID := int64(990002)
	mockItem(t, itemID, receiverID) // item authored by receiver, so PM goes TO receiver
	defer cleanMockItem(t, itemID)

	// Sender sends PM about receiver's item → PM receiver = item owner = receiverID.
	sendResp := testutil.DoPost(t, "/api/v1/pm/send", map[string]interface{}{
		"receiver_id": receiver["agent_id"],
		"item_id":     strconv.FormatInt(itemID, 10),
		"content":     "realtime push test",
	}, senderToken)
	if int(sendResp["code"].(float64)) != 0 {
		t.Fatalf("send PM failed: %v", sendResp["msg"])
	}

	msg := readPush(t, ws, 10*time.Second)
	if msg["type"] != "pm_push" {
		t.Fatalf("expected type pm_push, got %v", msg["type"])
	}
	data := msg["data"].(map[string]interface{})
	messages := data["messages"].([]interface{})
	if len(messages) == 0 {
		t.Fatal("expected at least 1 message in realtime push")
	}
	first := messages[0].(map[string]interface{})
	if first["content"] != "realtime push test" {
		t.Fatalf("expected content 'realtime push test', got %v", first["content"])
	}

	cleanPMData(t, senderID, receiverID)
}

func TestWSConnectionReplacement(t *testing.T) {
	testutil.WaitForAPI(t)
	waitForWS(t)

	emails := []string{"ws_replace@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agent := testutil.RegisterAgent(t, "ws_replace@test.com", "Replace Agent", "tests replacement")
	agentID, _ := strconv.ParseInt(agent["agent_id"].(string), 10, 64)
	token := agent["token"].(string)

	cleanPMData(t, agentID)

	// First connection.
	ws1 := dialWS(t, token, 0)
	defer ws1.Close()

	time.Sleep(300 * time.Millisecond)

	// Second connection — should evict ws1.
	ws2 := dialWS(t, token, 0)
	defer ws2.Close()

	// ws1 should receive a close frame with code 4002.
	ws1.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := ws1.ReadMessage()
	if err == nil {
		t.Fatal("expected ws1 to be closed after replacement")
	}
	closeErr, ok := err.(*websocket.CloseError)
	if ok && closeErr.Code != 4002 {
		t.Fatalf("expected close code 4002, got %d", closeErr.Code)
	}

	cleanPMData(t, agentID)
}

func TestWS_InitialPushIncludesHistory(t *testing.T) {
	testutil.WaitForAPI(t)
	waitForWS(t)

	emails := []string{"ws_hist_author@test.com", "ws_hist_user@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	author := testutil.RegisterAgent(t, "ws_hist_author@test.com", "Hist Author", "author for history test")
	user := testutil.RegisterAgent(t, "ws_hist_user@test.com", "Hist User", "user for history test")

	authorID, _ := strconv.ParseInt(author["agent_id"].(string), 10, 64)
	userID, _ := strconv.ParseInt(user["agent_id"].(string), 10, 64)
	authorToken := author["token"].(string)
	userToken := user["token"].(string)

	cleanPMData(t, authorID, userID)
	defer cleanPMData(t, authorID, userID)

	itemID := int64(990100)
	mockItem(t, itemID, authorID) // author owns the item so PM goes TO author
	defer cleanMockItem(t, itemID)

	// User sends PM to author about the item.
	sendResp := testutil.DoPost(t, "/api/v1/pm/send", map[string]interface{}{
		"receiver_id": author["agent_id"],
		"item_id":     strconv.FormatInt(itemID, 10),
		"content":     "historical msg",
	}, userToken)
	if int(sendResp["code"].(float64)) != 0 {
		t.Fatalf("send PM failed: %v", sendResp["msg"])
	}

	// Author fetches (reads) the message so it becomes history (unread count → 0).
	testutil.DoGet(t, "/api/v1/pm/fetch", authorToken)

	// Author dials WS — should receive initial push with history_messages.
	ws := dialWS(t, authorToken, 0)
	defer ws.Close()

	envelope := readPush(t, ws, 10*time.Second)

	if envelope["type"] != "pm_push" {
		t.Fatalf("expected type pm_push, got %v", envelope["type"])
	}

	data, ok := envelope["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be a map, got: %T %v", envelope["data"], envelope["data"])
	}

	// history_messages must be present and non-empty.
	rawHistory, hasHistory := data["history_messages"]
	if !hasHistory {
		t.Fatal("expected history_messages in initial push, key missing")
	}
	historyList, ok := rawHistory.([]interface{})
	if !ok || len(historyList) == 0 {
		t.Fatalf("expected non-empty history_messages, got: %v", rawHistory)
	}
	found := false
	for _, item := range historyList {
		m, ok := item.(map[string]interface{})
		if ok && m["content"] == "historical msg" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("history_messages did not contain 'historical msg', got: %v", historyList)
	}

	// messages (unread) must be empty — author already read the message.
	if rawMsgs, hasMsgs := data["messages"]; hasMsgs {
		if list, ok := rawMsgs.([]interface{}); ok && len(list) > 0 {
			t.Errorf("expected empty messages (all read), got: %v", list)
		}
	}
}

func TestWS_IncrementPushHasNoHistory(t *testing.T) {
	testutil.WaitForAPI(t)
	waitForWS(t)

	emails := []string{"ws_incr_a@test.com", "ws_incr_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	a := testutil.RegisterAgent(t, "ws_incr_a@test.com", "Incr A", "agent a for increment test")
	b := testutil.RegisterAgent(t, "ws_incr_b@test.com", "Incr B", "agent b for increment test")

	aID, _ := strconv.ParseInt(a["agent_id"].(string), 10, 64)
	bID, _ := strconv.ParseInt(b["agent_id"].(string), 10, 64)
	aToken := a["token"].(string)
	bToken := b["token"].(string)

	cleanPMData(t, aID, bID)
	defer cleanPMData(t, aID, bID)

	itemID := int64(990101)
	mockItem(t, itemID, aID) // a owns the item so PM goes TO a
	defer cleanMockItem(t, itemID)

	// a dials WS fresh — no history, no unread, so pushInitial sends nothing.
	ws := dialWS(t, aToken, 0)
	defer ws.Close()

	// Wait for pub/sub subscription to settle (mirrors TestWSRealtimePush).
	time.Sleep(500 * time.Millisecond)

	// b sends PM to a about the item.
	sendResp := testutil.DoPost(t, "/api/v1/pm/send", map[string]interface{}{
		"receiver_id": a["agent_id"],
		"item_id":     strconv.FormatInt(itemID, 10),
		"content":     "new msg",
	}, bToken)
	if int(sendResp["code"].(float64)) != 0 {
		t.Fatalf("send PM failed: %v", sendResp["msg"])
	}

	// Read the realtime (increment) push.
	envelope := readPush(t, ws, 10*time.Second)

	if envelope["type"] != "pm_push" {
		t.Fatalf("expected type pm_push, got %v", envelope["type"])
	}

	data, ok := envelope["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be a map, got: %T %v", envelope["data"], envelope["data"])
	}

	// messages must contain the new message.
	messages, ok := data["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		t.Fatalf("expected at least 1 message in increment push, got: %v", data["messages"])
	}
	found := false
	for _, item := range messages {
		m, ok := item.(map[string]interface{})
		if ok && m["content"] == "new msg" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("messages did not contain 'new msg', got: %v", messages)
	}

	// history_messages must be absent or empty in an increment push.
	if raw, has := data["history_messages"]; has {
		if list, ok := raw.([]interface{}); ok && len(list) > 0 {
			t.Errorf("increment push should not contain history_messages, got: %v", list)
		}
	}
}

func TestWS_InitialPushIncludesPendingFriendRequests(t *testing.T) {
	testutil.WaitForAPI(t)
	waitForWS(t)

	emails := []string{"ws_fr_recv@test.com", "ws_fr_s1@test.com", "ws_fr_s2@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	r := testutil.RegisterAgent(t, "ws_fr_recv@test.com", "R", "recv")
	s1 := testutil.RegisterAgent(t, "ws_fr_s1@test.com", "S1", "sender1")
	s2 := testutil.RegisterAgent(t, "ws_fr_s2@test.com", "S2", "sender2")

	rID, _ := strconv.ParseInt(r["agent_id"].(string), 10, 64)
	s1ID, _ := strconv.ParseInt(s1["agent_id"].(string), 10, 64)
	s2ID, _ := strconv.ParseInt(s2["agent_id"].(string), 10, 64)

	cleanPMData(t, rID, s1ID, s2ID)
	defer cleanPMData(t, rID, s1ID, s2ID)
	defer testutil.TestDB.Exec("DELETE FROM friend_requests WHERE to_uid = $1", rID)

	for _, tok := range []string{s1["token"].(string), s2["token"].(string)} {
		resp := testutil.DoPost(t, "/api/v1/relations/apply", map[string]interface{}{
			"to_uid":   r["agent_id"],
			"greeting": "hi",
		}, tok)
		if int(resp["code"].(float64)) != 0 {
			t.Fatalf("apply failed: %v", resp["msg"])
		}
	}

	ws := dialWS(t, r["token"].(string), 0)
	defer ws.Close()

	envelope := readPush(t, ws, 10*time.Second)
	data, ok := envelope["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data map, got %T %v", envelope["data"], envelope["data"])
	}

	raw, has := data["friend_requests"]
	if !has {
		t.Fatal("expected friend_requests in initial push")
	}
	list, ok := raw.([]interface{})
	if !ok || len(list) != 2 {
		t.Fatalf("expected 2 friend_requests, got: %v", raw)
	}

	countRaw, ok := data["friend_requests_count"]
	if !ok {
		t.Fatal("expected friend_requests_count in initial push")
	}
	if count, ok := countRaw.(float64); !ok || int64(count) != 2 {
		t.Fatalf("friend_requests_count: want 2, got %v", countRaw)
	}

	first, _ := list[0].(map[string]interface{})
	if first["from_uid"] != strconv.FormatInt(s2ID, 10) {
		t.Errorf("first friend_request should be from s2 (%d), got from_uid=%v", s2ID, first["from_uid"])
	}
	if first["from_name"] != "S2" {
		t.Errorf("first friend_request from_name: want S2, got %v", first["from_name"])
	}
	if first["greeting"] != "hi" {
		t.Errorf("first friend_request greeting: want hi, got %v", first["greeting"])
	}
	if rid, _ := first["request_id"].(string); rid == "" {
		t.Errorf("first friend_request request_id should be non-empty, got %v", first["request_id"])
	}
}
