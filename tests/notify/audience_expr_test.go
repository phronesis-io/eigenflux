package notify_test

import (
	"net/http"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

func TestAudienceExpressionFiltering(t *testing.T) {
	testutil.WaitForAPI(t)
	agent := testutil.RegisterAgent(t, "audience_expr_filter@test.com", "AudienceExprFilter", "test")
	token := agent["token"].(string)

	notif := createSystemNotificationWithExpr(t, "announcement", "upgrade notice", 1, 0, 0, `skill_ver_num < 3`)
	defer offlineSystemNotification(t, notif.NotificationID)

	time.Sleep(200 * time.Millisecond)

	// Feed with X-Skill-Ver: 0.0.2 (skill_ver_num=2) → should see notification
	feedData := testutil.DoGetWithHeaders(t, "/api/v1/items/feed?action=refresh", token,
		map[string]string{"X-Skill-Ver": "0.0.2"})
	notifications := feedNotifications(t, feedData["data"].(map[string]interface{}))
	found := false
	for _, n := range notifications {
		if n["notification_id"] == notif.NotificationID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected notification for skill_ver_num=2 < 3")
	}

	// Feed with X-Skill-Ver: 0.0.3 (skill_ver_num=3) → should NOT see notification
	feedData2 := testutil.DoGetWithHeaders(t, "/api/v1/items/feed?action=refresh", token,
		map[string]string{"X-Skill-Ver": "0.0.3"})
	notifications2 := feedNotifications(t, feedData2["data"].(map[string]interface{}))
	for _, n := range notifications2 {
		if n["notification_id"] == notif.NotificationID {
			t.Fatal("should NOT see notification for skill_ver_num=3")
		}
	}
}

func TestAudienceExpressionNoHeader(t *testing.T) {
	testutil.WaitForAPI(t)
	agent := testutil.RegisterAgent(t, "audience_expr_noheader@test.com", "AudienceExprNoHeader", "test")
	token := agent["token"].(string)

	notif := createSystemNotificationWithExpr(t, "announcement", "no header test", 1, 0, 0, `skill_ver_num < 3`)
	defer offlineSystemNotification(t, notif.NotificationID)
	time.Sleep(200 * time.Millisecond)

	feedData := testutil.DoGet(t, "/api/v1/items/feed?action=refresh", token)
	notifications := feedNotifications(t, feedData["data"].(map[string]interface{}))
	found := false
	for _, n := range notifications {
		if n["notification_id"] == notif.NotificationID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected notification when no X-Skill-Ver header (skill_ver_num=0 < 3)")
	}
}

func TestAudienceExpressionCompound(t *testing.T) {
	testutil.WaitForAPI(t)
	agent := testutil.RegisterAgent(t, "audience_expr_compound@test.com", "AudienceExprCompound", "test")
	token := agent["token"].(string)

	notif := createSystemNotificationWithExpr(t, "announcement", "compound test", 1, 0, 0, `skill_ver_num > 0 && skill_ver_num < 3`)
	defer offlineSystemNotification(t, notif.NotificationID)
	time.Sleep(200 * time.Millisecond)

	feedData := testutil.DoGet(t, "/api/v1/items/feed?action=refresh", token)
	notifications := feedNotifications(t, feedData["data"].(map[string]interface{}))
	for _, n := range notifications {
		if n["notification_id"] == notif.NotificationID {
			t.Fatal("should NOT see notification when no header with compound expression")
		}
	}
}

func TestAudienceExpressionEmpty(t *testing.T) {
	testutil.WaitForAPI(t)
	agent := testutil.RegisterAgent(t, "audience_expr_empty@test.com", "AudienceExprEmpty", "test")
	token := agent["token"].(string)

	notif := createSystemNotificationWithExpr(t, "announcement", "broadcast test", 1, 0, 0, "")
	defer offlineSystemNotification(t, notif.NotificationID)
	time.Sleep(200 * time.Millisecond)

	feedData := testutil.DoGet(t, "/api/v1/items/feed?action=refresh", token)
	notifications := feedNotifications(t, feedData["data"].(map[string]interface{}))
	found := false
	for _, n := range notifications {
		if n["notification_id"] == notif.NotificationID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected notification with empty expression (broadcast)")
	}
}

func TestConsoleAudienceExpressionValidation(t *testing.T) {
	// Unknown variable → error
	body := map[string]interface{}{
		"type":                "announcement",
		"content":             "bad expr test",
		"status":              1,
		"audience_expression": "invalid_var_xyz == 1",
	}
	payload := doConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/system-notifications", body)
	var resp SystemNotificationResp
	mustDecodeResp(t, payload, &resp)
	if resp.Code == 0 {
		t.Fatal("expected error for invalid audience_expression (unknown variable)")
	}

	// Invalid syntax → error
	body2 := map[string]interface{}{
		"type":                "announcement",
		"content":             "bad syntax test",
		"status":              1,
		"audience_expression": "skill_ver_num <><> 3",
	}
	payload2 := doConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/system-notifications", body2)
	var resp2 SystemNotificationResp
	mustDecodeResp(t, payload2, &resp2)
	if resp2.Code == 0 {
		t.Fatal("expected error for invalid audience_expression (bad syntax)")
	}
}

func createSystemNotificationWithExpr(t *testing.T, notifType, content string, status int32, startAt, endAt int64, audienceExpr string) SystemNotificationInfo {
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
	if audienceExpr != "" {
		body["audience_expression"] = audienceExpr
	}
	payload := doConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/system-notifications", body)
	var resp SystemNotificationResp
	mustDecodeResp(t, payload, &resp)
	if resp.Code != 0 || resp.Data == nil {
		t.Fatalf("create system notification with expr failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return resp.Data.Notification
}
