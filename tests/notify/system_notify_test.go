package notify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/tests/testutil"
)

var consoleBaseURL = resolveConsoleBaseURL()

func TestMain(m *testing.M) {
	testutil.RunTestMain(m)
}

func resolveConsoleBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("CONSOLE_API_BASE_URL")); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	cfg := config.Load()
	return fmt.Sprintf("http://localhost:%d", cfg.ConsoleApiPort)
}

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

type SystemNotificationInfo struct {
	NotificationID     string `json:"notification_id"`
	Type               string `json:"type"`
	Content            string `json:"content"`
	Status             int32  `json:"status"`
	AudienceType       string `json:"audience_type"`
	AudienceExpression string `json:"audience_expression"`
	StartAt            int64  `json:"start_at"`
	EndAt              int64  `json:"end_at"`
	OfflineAt          int64  `json:"offline_at"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
}

type SystemNotificationData struct {
	Notification SystemNotificationInfo `json:"notification"`
}

type SystemNotificationResp struct {
	Code int32                   `json:"code"`
	Msg  string                  `json:"msg"`
	Data *SystemNotificationData `json:"data"`
}

type ListSystemNotificationsData struct {
	Notifications []SystemNotificationInfo `json:"notifications"`
	Total         int64                    `json:"total"`
	Page          int32                    `json:"page"`
	PageSize      int32                    `json:"page_size"`
}

type ListSystemNotificationsResp struct {
	Code int32                        `json:"code"`
	Msg  string                       `json:"msg"`
	Data *ListSystemNotificationsData `json:"data"`
}

// ---------------------------------------------------------------------------
// Console API helpers
// ---------------------------------------------------------------------------

func createSystemNotification(t *testing.T, notifType, content string, status int32, startAt, endAt int64) SystemNotificationInfo {
	t.Helper()
	body := map[string]interface{}{
		"type":    notifType,
		"content": content,
	}
	if status != 0 {
		body["status"] = status
	}
	if startAt != 0 {
		body["start_at"] = startAt
	}
	if endAt != 0 {
		body["end_at"] = endAt
	}
	payload := doConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/system-notifications", body)
	var resp SystemNotificationResp
	mustDecodeResp(t, payload, &resp)
	if resp.Code != 0 || resp.Data == nil {
		t.Fatalf("create system notification failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return resp.Data.Notification
}

func updateSystemNotification(t *testing.T, notificationID string, updates map[string]interface{}) SystemNotificationInfo {
	t.Helper()
	payload := doConsoleJSONRequest(t, http.MethodPut,
		"/console/api/v1/system-notifications/"+notificationID, updates)
	var resp SystemNotificationResp
	mustDecodeResp(t, payload, &resp)
	if resp.Code != 0 || resp.Data == nil {
		t.Fatalf("update system notification failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return resp.Data.Notification
}

func offlineSystemNotification(t *testing.T, notificationID string) SystemNotificationInfo {
	t.Helper()
	payload := doConsoleJSONRequest(t, http.MethodPost,
		"/console/api/v1/system-notifications/"+notificationID+"/offline", nil)
	var resp SystemNotificationResp
	mustDecodeResp(t, payload, &resp)
	if resp.Code != 0 || resp.Data == nil {
		t.Fatalf("offline system notification failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return resp.Data.Notification
}

func listSystemNotifications(t *testing.T, page, pageSize int, statusFilter *int) ListSystemNotificationsResp {
	t.Helper()
	path := fmt.Sprintf("/console/api/v1/system-notifications?page=%d&page_size=%d", page, pageSize)
	if statusFilter != nil {
		path += fmt.Sprintf("&status=%d", *statusFilter)
	}
	payload := doConsoleRequest(t, http.MethodGet, path, nil)
	var resp ListSystemNotificationsResp
	mustDecodeResp(t, payload, &resp)
	if resp.Code != 0 {
		t.Fatalf("list system notifications failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return resp
}

// ---------------------------------------------------------------------------
// HTTP / decode helpers
// ---------------------------------------------------------------------------

func doConsoleRequest(t *testing.T, method, path string, body io.Reader) []byte {
	t.Helper()
	req, err := http.NewRequest(method, consoleBaseURL+path, body)
	if err != nil {
		t.Fatalf("new request failed: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("Console API not running: %v", err)
		return nil
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body failed: %v", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		t.Skipf("endpoint not found: %s %s", method, path)
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.StatusCode, string(payload))
	}
	return payload
}

func doConsoleJSONRequest(t *testing.T, method, path string, body interface{}) []byte {
	t.Helper()
	if body == nil {
		return doConsoleRequest(t, method, path, nil)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request failed: %v", err)
	}
	return doConsoleRequest(t, method, path, bytes.NewReader(payload))
}

func mustDecodeResp(t *testing.T, payload []byte, target interface{}) {
	t.Helper()
	if err := json.Unmarshal(payload, target); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, string(payload))
	}
}

// ---------------------------------------------------------------------------
// Feed / delivery helpers
// ---------------------------------------------------------------------------

func feedNotifications(t *testing.T, feedData map[string]interface{}) []map[string]interface{} {
	t.Helper()
	raw, ok := feedData["notifications"].([]interface{})
	if !ok || len(raw) == 0 {
		return nil
	}
	result := make([]map[string]interface{}, len(raw))
	for i, v := range raw {
		result[i] = v.(map[string]interface{})
	}
	return result
}

func waitForNotificationDelivery(t *testing.T, sourceType string, sourceID, agentID int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var count int
		err := testutil.TestDB.QueryRow(
			"SELECT COUNT(*) FROM notification_deliveries WHERE source_type = $1 AND source_id = $2 AND agent_id = $3",
			sourceType, sourceID, agentID,
		).Scan(&count)
		if err == nil && count > 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for delivery row: source_type=%s source_id=%d agent_id=%d",
		sourceType, sourceID, agentID)
}

// ---------------------------------------------------------------------------
// Test data cleanup
// ---------------------------------------------------------------------------

func cleanNotifyTestData(t *testing.T) {
	t.Helper()
	testutil.CleanTestData(t)
	testutil.TestDB.Exec("DELETE FROM notification_deliveries")
	testutil.TestDB.Exec("DELETE FROM system_notifications")
	rdb := testutil.GetTestRedis()
	ctx := context.Background()
	keys, _ := rdb.Keys(ctx, "notify:system:*").Result()
	if len(keys) > 0 {
		rdb.Del(ctx, keys...)
	}
	keys, _ = rdb.Keys(ctx, "notify:pending:*").Result()
	if len(keys) > 0 {
		rdb.Del(ctx, keys...)
	}
}

// ---------------------------------------------------------------------------
// Tests: Console CRUD
// ---------------------------------------------------------------------------

func TestSystemNotificationConsoleCRUD(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanNotifyTestData(t)

	t.Log("=== Create active notification ===")
	created := createSystemNotification(t, "system", "Platform maintenance at 02:00 UTC", 1, 0, 0)
	if created.NotificationID == "" {
		t.Fatalf("expected non-empty notification_id")
	}
	if created.Type != "system" {
		t.Fatalf("expected type=system, got %s", created.Type)
	}
	if created.Content != "Platform maintenance at 02:00 UTC" {
		t.Fatalf("expected content match, got %s", created.Content)
	}
	if created.Status != 1 {
		t.Fatalf("expected status=1 (active), got %d", created.Status)
	}
	if created.AudienceType != "broadcast" {
		t.Fatalf("expected audience_type=broadcast, got %s", created.AudienceType)
	}

	t.Log("=== List all notifications ===")
	listed := listSystemNotifications(t, 1, 20, nil)
	if listed.Data.Total < 1 {
		t.Fatalf("expected at least 1 notification, got total=%d", listed.Data.Total)
	}
	found := false
	for _, n := range listed.Data.Notifications {
		if n.NotificationID == created.NotificationID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created notification %s not found in list", created.NotificationID)
	}

	t.Log("=== List with status=1 filter ===")
	activeFilter := 1
	listedActive := listSystemNotifications(t, 1, 20, &activeFilter)
	found = false
	for _, n := range listedActive.Data.Notifications {
		if n.NotificationID == created.NotificationID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("active notification not found when filtering status=1")
	}

	t.Log("=== Update content ===")
	updated := updateSystemNotification(t, created.NotificationID, map[string]interface{}{
		"content": "Maintenance rescheduled to 04:00 UTC",
	})
	if updated.Content != "Maintenance rescheduled to 04:00 UTC" {
		t.Fatalf("expected updated content, got %s", updated.Content)
	}
	if updated.Status != 1 {
		t.Fatalf("status should remain 1 after content update, got %d", updated.Status)
	}

	t.Log("=== Offline notification ===")
	offlined := offlineSystemNotification(t, created.NotificationID)
	if offlined.Status != 2 {
		t.Fatalf("expected status=2 after offline, got %d", offlined.Status)
	}
	if offlined.OfflineAt == 0 {
		t.Fatalf("expected non-zero offline_at")
	}

	t.Log("=== Verify offline appears in status=2 list ===")
	offlineFilter := 2
	listedOffline := listSystemNotifications(t, 1, 20, &offlineFilter)
	found = false
	for _, n := range listedOffline.Data.Notifications {
		if n.NotificationID == created.NotificationID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("offlined notification not found when filtering status=2")
	}
}

// ---------------------------------------------------------------------------
// Tests: Feed delivery and deduplication
// ---------------------------------------------------------------------------

func TestSystemNotificationDeliveryAndDedup(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanNotifyTestData(t)

	t.Log("=== Register two agents ===")
	agentA := testutil.RegisterAgent(t, "sysnotify_a@test.com", "SysNotifyA", "test agent A")
	tokenA := agentA["token"].(string)
	agentAID := testutil.MustID(t, agentA["agent_id"], "agent_id")

	agentB := testutil.RegisterAgent(t, "sysnotify_b@test.com", "SysNotifyB", "test agent B")
	tokenB := agentB["token"].(string)
	agentBID := testutil.MustID(t, agentB["agent_id"], "agent_id")

	t.Log("=== Create active broadcast notification ===")
	created := createSystemNotification(t, "announcement", "Platform maintenance at 02:00 UTC", 1, 0, 0)
	notifID := testutil.MustID(t, created.NotificationID, "notification_id")

	t.Log("=== Agent A refresh → gets notification ===")
	feedA := testutil.WaitForFeedNotifications(t, tokenA, 1, 20*time.Second)
	notificationsA := feedNotifications(t, feedA)
	if len(notificationsA) != 1 {
		t.Fatalf("expected 1 notification for agent A, got %d", len(notificationsA))
	}
	if notificationsA[0]["content"] != "Platform maintenance at 02:00 UTC" {
		t.Fatalf("unexpected content: %v", notificationsA[0]["content"])
	}
	if notificationsA[0]["type"] != "announcement" {
		t.Fatalf("expected type=announcement, got %v", notificationsA[0]["type"])
	}
	if notificationsA[0]["source_type"] != "system" {
		t.Fatalf("expected source_type=system, got %v", notificationsA[0]["source_type"])
	}

	t.Log("=== Wait for delivery record (agent A) ===")
	waitForNotificationDelivery(t, "system", notifID, agentAID, 15*time.Second)

	t.Log("=== Agent A refresh again → no notification (dedup) ===")
	feedA2 := testutil.FetchFeedRefresh(t, tokenA, 20)
	if n := feedNotifications(t, feedA2); len(n) > 0 {
		t.Fatalf("expected 0 notifications after ack, got %d", len(n))
	}

	t.Log("=== Agent B refresh → gets same notification (broadcast) ===")
	feedB := testutil.WaitForFeedNotifications(t, tokenB, 1, 20*time.Second)
	notificationsB := feedNotifications(t, feedB)
	if len(notificationsB) != 1 {
		t.Fatalf("expected 1 notification for agent B, got %d", len(notificationsB))
	}
	if notificationsB[0]["content"] != "Platform maintenance at 02:00 UTC" {
		t.Fatalf("agent B got wrong content: %v", notificationsB[0]["content"])
	}

	t.Log("=== Wait for delivery record (agent B) ===")
	waitForNotificationDelivery(t, "system", notifID, agentBID, 15*time.Second)

	t.Log("=== Agent B refresh again → no notification (dedup) ===")
	feedB2 := testutil.FetchFeedRefresh(t, tokenB, 20)
	if n := feedNotifications(t, feedB2); len(n) > 0 {
		t.Fatalf("expected 0 notifications for agent B after ack, got %d", len(n))
	}
}

// ---------------------------------------------------------------------------
// Tests: load_more never returns notifications
// ---------------------------------------------------------------------------

func TestSystemNotificationLoadMoreExcluded(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanNotifyTestData(t)

	agent := testutil.RegisterAgent(t, "sysnotify_lm@test.com", "SysNotifyLM", "test")
	token := agent["token"].(string)

	createSystemNotification(t, "system", "Should not appear in load_more", 1, 0, 0)

	t.Log("=== load_more must not return notifications ===")
	feed := testutil.FetchFeedLoadMore(t, token, 20)
	if n, ok := feed["notifications"].([]interface{}); ok && len(n) > 0 {
		t.Fatalf("expected no notifications on load_more, got %d", len(n))
	}
}

// ---------------------------------------------------------------------------
// Tests: Draft notification not delivered
// ---------------------------------------------------------------------------

func TestSystemNotificationDraftNotDelivered(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanNotifyTestData(t)

	agent := testutil.RegisterAgent(t, "sysnotify_draft@test.com", "SysNotifyDraft", "test")
	token := agent["token"].(string)

	t.Log("=== Create draft notification (status=0, default) ===")
	createSystemNotification(t, "system", "Draft content", 0, 0, 0)

	t.Log("=== Refresh should not return draft ===")
	feed := testutil.FetchFeedRefresh(t, token, 20)
	if n := feedNotifications(t, feed); len(n) > 0 {
		t.Fatalf("expected 0 notifications for draft, got %d", len(n))
	}
}

// ---------------------------------------------------------------------------
// Tests: Offlined notification stops delivery
// ---------------------------------------------------------------------------

func TestSystemNotificationOfflineStopsDelivery(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanNotifyTestData(t)

	agent := testutil.RegisterAgent(t, "sysnotify_off@test.com", "SysNotifyOff", "test")
	token := agent["token"].(string)

	t.Log("=== Create active notification then immediately offline it ===")
	created := createSystemNotification(t, "system", "Will be offlined", 1, 0, 0)
	offlineSystemNotification(t, created.NotificationID)

	// Brief wait for Redis state to reflect offline
	time.Sleep(1 * time.Second)

	t.Log("=== Refresh should not return offlined notification ===")
	feed := testutil.FetchFeedRefresh(t, token, 20)
	if n := feedNotifications(t, feed); len(n) > 0 {
		t.Fatalf("expected 0 notifications for offlined notification, got %d", len(n))
	}
}

// ---------------------------------------------------------------------------
// Tests: Time window filtering (start_at / end_at)
// ---------------------------------------------------------------------------

func TestSystemNotificationTimeWindow(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanNotifyTestData(t)

	agent := testutil.RegisterAgent(t, "sysnotify_tw@test.com", "SysNotifyTW", "test")
	token := agent["token"].(string)

	now := time.Now().UnixMilli()

	t.Log("=== Create notification with start_at in future (1 hour from now) ===")
	createSystemNotification(t, "system", "Future start", 1, now+3600_000, 0)

	t.Log("=== Create notification with end_at in past (expired 1 hour ago) ===")
	createSystemNotification(t, "system", "Past end", 1, now-7200_000, now-3600_000)

	t.Log("=== Refresh should return neither ===")
	feed := testutil.FetchFeedRefresh(t, token, 20)
	if n := feedNotifications(t, feed); len(n) > 0 {
		for _, notif := range n {
			t.Logf("unexpected notification: content=%v", notif["content"])
		}
		t.Fatalf("expected 0 notifications, got %d", len(n))
	}

	t.Log("=== Create notification within window (started 1h ago, ends 1h later) ===")
	createSystemNotification(t, "system", "Active window", 1, now-3600_000, now+3600_000)

	t.Log("=== Refresh should return only the in-window notification ===")
	feed2 := testutil.WaitForFeedNotifications(t, token, 1, 20*time.Second)
	notifications := feedNotifications(t, feed2)
	if len(notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifications))
	}
	if notifications[0]["content"] != "Active window" {
		t.Fatalf("expected 'Active window' content, got %v", notifications[0]["content"])
	}
}

// ---------------------------------------------------------------------------
// Tests: Updated content visible to undelivered agents
// ---------------------------------------------------------------------------

func TestSystemNotificationUpdateContent(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanNotifyTestData(t)

	agentA := testutil.RegisterAgent(t, "sysnotify_upd_a@test.com", "SysNotifyUpdA", "test")
	tokenA := agentA["token"].(string)
	agentAID := testutil.MustID(t, agentA["agent_id"], "agent_id")

	agentB := testutil.RegisterAgent(t, "sysnotify_upd_b@test.com", "SysNotifyUpdB", "test")
	tokenB := agentB["token"].(string)

	t.Log("=== Create active notification v1 ===")
	created := createSystemNotification(t, "announcement", "Content v1", 1, 0, 0)
	notifID := testutil.MustID(t, created.NotificationID, "notification_id")

	t.Log("=== Agent A gets v1 ===")
	feedA := testutil.WaitForFeedNotifications(t, tokenA, 1, 20*time.Second)
	notificationsA := feedNotifications(t, feedA)
	if notificationsA[0]["content"] != "Content v1" {
		t.Fatalf("expected 'Content v1', got %v", notificationsA[0]["content"])
	}
	waitForNotificationDelivery(t, "system", notifID, agentAID, 15*time.Second)

	t.Log("=== Update content to v2 ===")
	updateSystemNotification(t, created.NotificationID, map[string]interface{}{
		"content": "Content v2",
	})

	t.Log("=== Agent A refresh → NOT returned again (already delivered) ===")
	feedA2 := testutil.FetchFeedRefresh(t, tokenA, 20)
	if n := feedNotifications(t, feedA2); len(n) > 0 {
		t.Fatalf("agent A should not see notification again after update, got %d", len(n))
	}

	t.Log("=== Agent B refresh → gets v2 (updated content) ===")
	feedB := testutil.WaitForFeedNotifications(t, tokenB, 1, 20*time.Second)
	notificationsB := feedNotifications(t, feedB)
	if notificationsB[0]["content"] != "Content v2" {
		t.Fatalf("expected agent B to see 'Content v2', got %v", notificationsB[0]["content"])
	}
}

// ---------------------------------------------------------------------------
// Tests: Multiple active notifications returned together
// ---------------------------------------------------------------------------

func TestSystemNotificationMultipleActive(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanNotifyTestData(t)

	agent := testutil.RegisterAgent(t, "sysnotify_multi@test.com", "SysNotifyMulti", "test")
	token := agent["token"].(string)
	agentID := testutil.MustID(t, agent["agent_id"], "agent_id")

	t.Log("=== Create two active notifications ===")
	n1 := createSystemNotification(t, "system", "Notification one", 1, 0, 0)
	n2 := createSystemNotification(t, "announcement", "Notification two", 1, 0, 0)
	n1ID := testutil.MustID(t, n1.NotificationID, "notification_id")
	n2ID := testutil.MustID(t, n2.NotificationID, "notification_id")

	t.Log("=== Refresh returns both ===")
	feed := testutil.WaitForFeedNotifications(t, token, 2, 20*time.Second)
	notifications := feedNotifications(t, feed)
	if len(notifications) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(notifications))
	}

	// Verify sorted by created_at ASC, notification_id ASC
	id0 := testutil.MustID(t, notifications[0]["notification_id"], "notification_id")
	id1 := testutil.MustID(t, notifications[1]["notification_id"], "notification_id")
	if id0 > id1 {
		t.Fatalf("expected notifications sorted by id ASC, got %d before %d", id0, id1)
	}

	t.Log("=== Wait for both deliveries ===")
	waitForNotificationDelivery(t, "system", n1ID, agentID, 15*time.Second)
	waitForNotificationDelivery(t, "system", n2ID, agentID, 15*time.Second)

	t.Log("=== Refresh again → only system (persistent) remains ===")
	feed2 := testutil.WaitForFeedNotifications(t, token, 1, 20*time.Second)
	notifications2 := feedNotifications(t, feed2)
	if len(notifications2) != 1 {
		t.Fatalf("expected 1 persistent notification after ack, got %d", len(notifications2))
	}
	if notifications2[0]["type"] != "system" {
		t.Fatalf("expected persistent notification type=system, got %v", notifications2[0]["type"])
	}
}

// ---------------------------------------------------------------------------
// Tests: Delivery uniqueness (idempotent ack)
// ---------------------------------------------------------------------------

func TestSystemNotificationDeliveryUniqueness(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanNotifyTestData(t)

	agent := testutil.RegisterAgent(t, "sysnotify_uniq@test.com", "SysNotifyUniq", "test")
	token := agent["token"].(string)
	agentID := testutil.MustID(t, agent["agent_id"], "agent_id")

	created := createSystemNotification(t, "announcement", "Uniqueness test", 1, 0, 0)
	notifID := testutil.MustID(t, created.NotificationID, "notification_id")

	t.Log("=== First refresh delivers notification ===")
	testutil.WaitForFeedNotifications(t, token, 1, 20*time.Second)
	waitForNotificationDelivery(t, "system", notifID, agentID, 15*time.Second)

	t.Log("=== Verify exactly 1 delivery row ===")
	var count int
	err := testutil.TestDB.QueryRow(
		"SELECT COUNT(*) FROM notification_deliveries WHERE source_type = 'system' AND source_id = $1 AND agent_id = $2",
		notifID, agentID,
	).Scan(&count)
	if err != nil {
		t.Fatalf("query delivery count failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 delivery row, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Tests: Persistent (type=system) notification survives ack
// ---------------------------------------------------------------------------

func TestPersistentSystemNotification(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanNotifyTestData(t)

	agent := testutil.RegisterAgent(t, "sysnotify_persist@test.com", "SysNotifyPersist", "test")
	token := agent["token"].(string)
	agentID := testutil.MustID(t, agent["agent_id"], "agent_id")

	t.Log("=== Create persistent (type=system) notification ===")
	created := createSystemNotification(t, "system", "Persistent notice", 1, 0, 0)
	notifID := testutil.MustID(t, created.NotificationID, "notification_id")

	t.Log("=== First refresh → gets notification ===")
	feed1 := testutil.WaitForFeedNotifications(t, token, 1, 20*time.Second)
	n1 := feedNotifications(t, feed1)
	if len(n1) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(n1))
	}
	if n1[0]["content"] != "Persistent notice" {
		t.Fatalf("unexpected content: %v", n1[0]["content"])
	}

	t.Log("=== Wait for delivery record (written for audit) ===")
	waitForNotificationDelivery(t, "system", notifID, agentID, 15*time.Second)

	t.Log("=== Second refresh → still gets notification (persistent) ===")
	feed2 := testutil.WaitForFeedNotifications(t, token, 1, 20*time.Second)
	n2 := feedNotifications(t, feed2)
	if len(n2) != 1 {
		t.Fatalf("expected 1 notification on second refresh, got %d", len(n2))
	}
	if n2[0]["content"] != "Persistent notice" {
		t.Fatalf("unexpected content on second refresh: %v", n2[0]["content"])
	}

	t.Log("=== Third refresh → still there ===")
	feed3 := testutil.WaitForFeedNotifications(t, token, 1, 20*time.Second)
	n3 := feedNotifications(t, feed3)
	if len(n3) != 1 {
		t.Fatalf("expected 1 notification on third refresh, got %d", len(n3))
	}

	t.Log("=== Offline the notification → stops appearing ===")
	offlineSystemNotification(t, created.NotificationID)
	time.Sleep(1 * time.Second)

	feed4 := testutil.FetchFeedRefresh(t, token, 20)
	if n := feedNotifications(t, feed4); len(n) > 0 {
		t.Fatalf("expected 0 notifications after offline, got %d", len(n))
	}
}

// ---------------------------------------------------------------------------
// Tests: Announcement (type=announcement) is one-time delivery
// ---------------------------------------------------------------------------

func TestAnnouncementOneTimeDelivery(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanNotifyTestData(t)

	agent := testutil.RegisterAgent(t, "sysnotify_announce@test.com", "SysNotifyAnnounce", "test")
	token := agent["token"].(string)
	agentID := testutil.MustID(t, agent["agent_id"], "agent_id")

	t.Log("=== Create announcement notification ===")
	created := createSystemNotification(t, "announcement", "One-time notice", 1, 0, 0)
	notifID := testutil.MustID(t, created.NotificationID, "notification_id")

	t.Log("=== First refresh → gets notification ===")
	feed1 := testutil.WaitForFeedNotifications(t, token, 1, 20*time.Second)
	n1 := feedNotifications(t, feed1)
	if len(n1) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(n1))
	}
	if n1[0]["content"] != "One-time notice" {
		t.Fatalf("unexpected content: %v", n1[0]["content"])
	}

	t.Log("=== Wait for delivery record ===")
	waitForNotificationDelivery(t, "system", notifID, agentID, 15*time.Second)

	t.Log("=== Second refresh → no notification (one-time, already delivered) ===")
	feed2 := testutil.FetchFeedRefresh(t, token, 20)
	if n := feedNotifications(t, feed2); len(n) > 0 {
		t.Fatalf("expected 0 notifications after ack for announcement, got %d", len(n))
	}
}
