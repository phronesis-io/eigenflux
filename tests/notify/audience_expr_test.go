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
	// Unknown variable with audience_type=expression → error
	body := map[string]interface{}{
		"type":                "announcement",
		"content":             "bad expr test",
		"status":              1,
		"audience_type":       "expression",
		"audience_expression": "invalid_var_xyz == 1",
	}
	payload := testutil.DoConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/system-notifications", body)
	var resp SystemNotificationResp
	testutil.MustDecodeResp(t, payload, &resp)
	if resp.Code == 0 {
		t.Fatal("expected error for invalid audience_expression (unknown variable)")
	}

	// Invalid syntax with audience_type=expression → error
	body2 := map[string]interface{}{
		"type":                "announcement",
		"content":             "bad syntax test",
		"status":              1,
		"audience_type":       "expression",
		"audience_expression": "skill_ver_num <><> 3",
	}
	payload2 := testutil.DoConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/system-notifications", body2)
	var resp2 SystemNotificationResp
	testutil.MustDecodeResp(t, payload2, &resp2)
	if resp2.Code == 0 {
		t.Fatal("expected error for invalid audience_expression (bad syntax)")
	}

	// audience_type=expression without expression → error
	body3 := map[string]interface{}{
		"type":          "announcement",
		"content":       "missing expr test",
		"status":        1,
		"audience_type": "expression",
	}
	payload3 := testutil.DoConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/system-notifications", body3)
	var resp3 SystemNotificationResp
	testutil.MustDecodeResp(t, payload3, &resp3)
	if resp3.Code == 0 {
		t.Fatal("expected error for expression type without expression")
	}
}

func TestUpdateAudienceExpressionWhitespaceTrimmed(t *testing.T) {
	testutil.WaitForAPI(t)

	// Create a broadcast notification (no expression).
	notif := createSystemNotification(t, "announcement", "ws trim test", 1, 0, 0)
	defer offlineSystemNotification(t, notif.NotificationID)

	// Update with whitespace-only audience_expression → should be stored as "".
	updated := updateSystemNotification(t, notif.NotificationID, map[string]interface{}{
		"audience_expression": "   \t\n  ",
	})
	if updated.AudienceExpression != "" {
		t.Fatalf("expected empty audience_expression after whitespace-only update, got %q", updated.AudienceExpression)
	}

	// Update with padded valid expression → stored trimmed.
	updated2 := updateSystemNotification(t, notif.NotificationID, map[string]interface{}{
		"audience_type":       "expression",
		"audience_expression": "  skill_ver_num < 5  ",
	})
	if updated2.AudienceExpression != "skill_ver_num < 5" {
		t.Fatalf("expected trimmed expression, got %q", updated2.AudienceExpression)
	}

	// Verify delivery still works (expression evaluates correctly after trim).
	agent := testutil.RegisterAgent(t, "ws_trim_verify@test.com", "WsTrimVerify", "test")
	token := agent["token"].(string)
	time.Sleep(200 * time.Millisecond)

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
		t.Fatal("expected notification delivery after trimmed expression update")
	}
}

func TestAudienceExpressionSkillVerString(t *testing.T) {
	testutil.WaitForAPI(t)
	agent := testutil.RegisterAgent(t, "audience_skillver_str@test.com", "AudienceSkillVerStr", "test")
	token := agent["token"].(string)

	notif := createSystemNotificationWithExpr(t, "announcement", "exact version match", 1, 0, 0, `skill_ver == "0.0.3"`)
	defer offlineSystemNotification(t, notif.NotificationID)
	time.Sleep(200 * time.Millisecond)

	// Matching version → delivered
	feedData := testutil.DoGetWithHeaders(t, "/api/v1/items/feed?action=refresh", token,
		map[string]string{"X-Skill-Ver": "0.0.3"})
	notifications := feedNotifications(t, feedData["data"].(map[string]interface{}))
	found := false
	for _, n := range notifications {
		if n["notification_id"] == notif.NotificationID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected notification for skill_ver == 0.0.3")
	}

	// Different version → NOT delivered
	feedData2 := testutil.DoGetWithHeaders(t, "/api/v1/items/feed?action=refresh", token,
		map[string]string{"X-Skill-Ver": "0.0.4"})
	notifications2 := feedNotifications(t, feedData2["data"].(map[string]interface{}))
	for _, n := range notifications2 {
		if n["notification_id"] == notif.NotificationID {
			t.Fatal("should NOT see notification for skill_ver 0.0.4")
		}
	}
}

func TestAudienceExpressionAgentID(t *testing.T) {
	testutil.WaitForAPI(t)
	agent1 := testutil.RegisterAgent(t, "audience_agentid_1@test.com", "AudienceAgent1", "test")
	agent2 := testutil.RegisterAgent(t, "audience_agentid_2@test.com", "AudienceAgent2", "test")
	token1 := agent1["token"].(string)
	token2 := agent2["token"].(string)
	agentID1 := agent1["agent_id"].(string)

	// Target agent1 only
	notif := createSystemNotificationWithExpr(t, "announcement", "agent targeted", 1, 0, 0, `agent_id == `+agentID1)
	defer offlineSystemNotification(t, notif.NotificationID)
	time.Sleep(200 * time.Millisecond)

	// Agent1 → delivered
	feedData := testutil.DoGet(t, "/api/v1/items/feed?action=refresh", token1)
	notifications := feedNotifications(t, feedData["data"].(map[string]interface{}))
	found := false
	for _, n := range notifications {
		if n["notification_id"] == notif.NotificationID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected notification for targeted agent_id")
	}

	// Agent2 → NOT delivered
	feedData2 := testutil.DoGet(t, "/api/v1/items/feed?action=refresh", token2)
	notifications2 := feedNotifications(t, feedData2["data"].(map[string]interface{}))
	for _, n := range notifications2 {
		if n["notification_id"] == notif.NotificationID {
			t.Fatal("should NOT see notification for non-targeted agent_id")
		}
	}
}

func TestAudienceExpressionEmail(t *testing.T) {
	testutil.WaitForAPI(t)
	targetEmail := "audience_email_match@test.com"
	agent1 := testutil.RegisterAgent(t, targetEmail, "AudienceEmailMatch", "test")
	agent2 := testutil.RegisterAgent(t, "audience_email_other@test.com", "AudienceEmailOther", "test")
	token1 := agent1["token"].(string)
	token2 := agent2["token"].(string)

	notif := createSystemNotificationWithExpr(t, "announcement", "email targeted", 1, 0, 0, `email == "`+targetEmail+`"`)
	defer offlineSystemNotification(t, notif.NotificationID)
	time.Sleep(200 * time.Millisecond)

	// Matching email → delivered
	feedData := testutil.DoGet(t, "/api/v1/items/feed?action=refresh", token1)
	notifications := feedNotifications(t, feedData["data"].(map[string]interface{}))
	found := false
	for _, n := range notifications {
		if n["notification_id"] == notif.NotificationID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected notification for matching email")
	}

	// Non-matching email → NOT delivered
	feedData2 := testutil.DoGet(t, "/api/v1/items/feed?action=refresh", token2)
	notifications2 := feedNotifications(t, feedData2["data"].(map[string]interface{}))
	for _, n := range notifications2 {
		if n["notification_id"] == notif.NotificationID {
			t.Fatal("should NOT see notification for non-matching email")
		}
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
		body["audience_type"] = "expression"
		body["audience_expression"] = audienceExpr
	}
	payload := testutil.DoConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/system-notifications", body)
	var resp SystemNotificationResp
	testutil.MustDecodeResp(t, payload, &resp)
	if resp.Code != 0 || resp.Data == nil {
		t.Fatalf("create system notification with expr failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return resp.Data.Notification
}
