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
}

func cleanMockItem(t *testing.T, itemID int64) {
	t.Helper()
	testutil.TestDB.Exec("DELETE FROM processed_items WHERE item_id = $1", itemID)
	testutil.TestDB.Exec("DELETE FROM raw_items WHERE item_id = $1", itemID)
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
	mockItem(t, itemID, senderID)
	defer cleanMockItem(t, itemID)

	// Send PM via HTTP before connecting WS.
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
	if msg["type"] != "pm_fetch" {
		t.Fatalf("expected type pm_fetch, got %v", msg["type"])
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
	mockItem(t, itemID, senderID)
	defer cleanMockItem(t, itemID)

	// Send PM via HTTP — should trigger real-time push.
	sendResp := testutil.DoPost(t, "/api/v1/pm/send", map[string]interface{}{
		"receiver_id": receiver["agent_id"],
		"item_id":     strconv.FormatInt(itemID, 10),
		"content":     "realtime push test",
	}, senderToken)
	if int(sendResp["code"].(float64)) != 0 {
		t.Fatalf("send PM failed: %v", sendResp["msg"])
	}

	msg := readPush(t, ws, 10*time.Second)
	if msg["type"] != "pm_fetch" {
		t.Fatalf("expected type pm_fetch, got %v", msg["type"])
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
