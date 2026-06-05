package console_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

const testEmail = "console-v1-test@test.com"

// ---------- Auth Guard Tests ----------

func TestConsoleEndpointsRequireAuth(t *testing.T) {
	testutil.WaitForAPI(t)

	authRequired := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/console/today"},
		{"GET", "/api/v1/console/activity-log"},
		{"GET", "/api/v1/console/activity-calendar"},
		{"GET", "/api/v1/console/highlights"},
		{"POST", "/api/v1/console/highlight-feedback"},
		{"GET", "/api/v1/console/settings"},
		{"PUT", "/api/v1/console/settings"},
		{"POST", "/api/v1/console/auth-code"},
	}

	for _, tc := range authRequired {
		t.Run(fmt.Sprintf("%s_%s_returns_401", tc.method, tc.path), func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, testutil.BaseURL+tc.path, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != 401 {
				t.Fatalf("expected 401 without auth, got %d", resp.StatusCode)
			}
		})
	}

	// Exchange should NOT require auth
	t.Run("POST_exchange_no_auth_not_401", func(t *testing.T) {
		req, _ := http.NewRequest("POST", testutil.BaseURL+"/api/v1/console/exchange", strings.NewReader(`{"code":"invalid"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode == 401 {
			t.Fatalf("exchange should NOT require auth, got 401")
		}
	})
}

// ---------- GetMe Extension ----------

func TestGetMeIncludesCountryAndKeywords(t *testing.T) {
	testutil.WaitForAPI(t)
	token, _, _ := testutil.LoginAndGetToken(t, testEmail)

	result := testutil.DoGet(t, "/api/v1/agents/me", token)
	code := int(result["code"].(float64))
	if code != 0 {
		t.Fatalf("expected code=0, got %d: %s", code, result["msg"])
	}

	data := result["data"].(map[string]interface{})
	profile := data["profile"].(map[string]interface{})

	if _, ok := profile["country"]; !ok {
		t.Fatal("profile missing 'country' field")
	}
	if _, ok := profile["keywords"]; !ok {
		t.Fatal("profile missing 'keywords' field")
	}

	keywords := profile["keywords"]
	if keywords == nil {
		t.Fatal("keywords should be an empty array, not null")
	}
	// Verify it's an array
	if _, ok := keywords.([]interface{}); !ok {
		t.Fatalf("keywords should be an array, got %T", keywords)
	}
}

// ---------- Console Today ----------

func TestConsoleTodayReturnsValidStructure(t *testing.T) {
	testutil.WaitForAPI(t)
	token, _, _ := testutil.LoginAndGetToken(t, testEmail)

	result := testutil.DoGet(t, "/api/v1/console/today", token)
	assertCode(t, result, 0)

	data := result["data"].(map[string]interface{})
	for _, key := range []string{
		"signals_scanned", "worth_reading", "days_active", "relations_formed",
		"unread_count", "broadcast_count", "mode", "last_sync_at",
	} {
		requireKey(t, data, key)
	}

	today := data["today"].(map[string]interface{})
	requireKey(t, today, "inbound")
	requireKey(t, today, "outbound")

	inbound := today["inbound"].(map[string]interface{})
	for _, key := range []string{
		"feeds_pulled", "items_scanned", "items_pushed", "you_marked_useful", "new_relations",
	} {
		requireKey(t, inbound, key)
	}

	outbound := today["outbound"].(map[string]interface{})
	for _, key := range []string{
		"broadcasts_sent", "total_reach", "replies_received",
		"them_marked_useful", "messages_sent", "feedbacks_given",
	} {
		requireKey(t, outbound, key)
	}
}

// ---------- Activity Log ----------

func TestConsoleActivityLogDefaultParams(t *testing.T) {
	testutil.WaitForAPI(t)
	token, _, _ := testutil.LoginAndGetToken(t, testEmail)

	result := testutil.DoGet(t, "/api/v1/console/activity-log", token)
	assertCode(t, result, 0)

	data := result["data"].(map[string]interface{})
	events, ok := data["events"].([]interface{})
	if !ok {
		t.Fatal("expected events array in response")
	}
	// Fresh agent may have empty events, that's ok
	t.Logf("activity log returned %d events", len(events))
}

func TestConsoleActivityLogRespectsLimit(t *testing.T) {
	testutil.WaitForAPI(t)
	token, _, _ := testutil.LoginAndGetToken(t, testEmail)

	result := testutil.DoGet(t, "/api/v1/console/activity-log?limit=5", token)
	assertCode(t, result, 0)

	data := result["data"].(map[string]interface{})
	events := data["events"].([]interface{})
	if len(events) > 5 {
		t.Fatalf("expected at most 5 events, got %d", len(events))
	}
}

func TestConsoleActivityLogRespectsHours(t *testing.T) {
	testutil.WaitForAPI(t)
	token, _, _ := testutil.LoginAndGetToken(t, testEmail)

	result := testutil.DoGet(t, "/api/v1/console/activity-log?hours=1", token)
	assertCode(t, result, 0)

	data := result["data"].(map[string]interface{})
	events := data["events"].([]interface{})
	// Verify all returned events are within 1 hour
	oneHourAgoMs := float64(time.Now().Add(-1*time.Hour).UnixMilli()) - 1000 // 1s buffer
	for i, e := range events {
		event := e.(map[string]interface{})
		createdAt, ok := event["created_at"].(float64)
		if !ok {
			continue
		}
		if createdAt < oneHourAgoMs {
			t.Fatalf("event[%d] created_at=%v is older than 1 hour", i, createdAt)
		}
	}
}

// ---------- Activity Calendar ----------

func TestConsoleActivityCalendarReturnsCalendar(t *testing.T) {
	testutil.WaitForAPI(t)
	token, _, _ := testutil.LoginAndGetToken(t, testEmail)

	result := testutil.DoGet(t, "/api/v1/console/activity-calendar?days=30", token)
	assertCode(t, result, 0)

	data := result["data"].(map[string]interface{})
	calendar, ok := data["calendar"].([]interface{})
	if !ok {
		t.Fatal("expected calendar array in response")
	}
	// Each entry should have date and count
	for i, entry := range calendar {
		e := entry.(map[string]interface{})
		if _, ok := e["date"]; !ok {
			t.Fatalf("calendar[%d] missing 'date'", i)
		}
		if _, ok := e["count"]; !ok {
			t.Fatalf("calendar[%d] missing 'count'", i)
		}
	}
	t.Logf("calendar returned %d entries", len(calendar))
}

// ---------- Highlights ----------

func TestConsoleHighlightsReturnsItems(t *testing.T) {
	testutil.WaitForAPI(t)
	token, _, _ := testutil.LoginAndGetToken(t, testEmail)

	result := testutil.DoGet(t, "/api/v1/console/highlights?limit=5", token)
	assertCode(t, result, 0)

	data := result["data"].(map[string]interface{})
	highlights, ok := data["highlights"].([]interface{})
	if !ok {
		t.Fatal("expected highlights array")
	}
	// impression_id lives on each highlight (per-delivery), not at the top level.
	for i, h := range highlights {
		hl := h.(map[string]interface{})
		requireKey(t, hl, "item_id")
		requireKey(t, hl, "impression_id")
		if i >= 2 {
			break
		}
	}
}

// ---------- Highlight Feedback ----------

func TestConsoleHighlightFeedbackUseful(t *testing.T) {
	testutil.WaitForAPI(t)
	token, _, _ := testutil.LoginAndGetToken(t, testEmail)

	result := testutil.DoPost(t, "/api/v1/console/highlight-feedback", map[string]interface{}{
		"item_id":  "1",
		"feedback": "useful",
	}, token)
	assertCode(t, result, 0)
}

func TestConsoleHighlightFeedbackSkip(t *testing.T) {
	testutil.WaitForAPI(t)
	token, _, _ := testutil.LoginAndGetToken(t, testEmail)

	result := testutil.DoPost(t, "/api/v1/console/highlight-feedback", map[string]interface{}{
		"item_id":  "1",
		"feedback": "skip",
	}, token)
	assertCode(t, result, 0)
}

func TestConsoleHighlightFeedbackInvalidType(t *testing.T) {
	testutil.WaitForAPI(t)
	token, _, _ := testutil.LoginAndGetToken(t, testEmail)

	result := testutil.DoPost(t, "/api/v1/console/highlight-feedback", map[string]interface{}{
		"item_id":  "1",
		"feedback": "invalid",
	}, token)
	code := int(result["code"].(float64))
	if code == 0 {
		t.Fatal("expected error for invalid feedback type, got code=0")
	}
}

// ---------- Settings ----------

func TestConsoleSettingsGetDefaults(t *testing.T) {
	testutil.WaitForAPI(t)
	email := fmt.Sprintf("console-settings-defaults-%d@test.com", time.Now().UnixNano()%1_000_000)
	token, _, _ := testutil.LoginAndGetToken(t, email)

	result := testutil.DoGet(t, "/api/v1/console/settings", token)
	assertCode(t, result, 0)

	data := result["data"].(map[string]interface{})
	recurringPublish, ok := data["recurring_publish"].(bool)
	if !ok || !recurringPublish {
		t.Fatalf("expected recurring_publish=true by default, got %v", data["recurring_publish"])
	}
	feedPoll := int(data["feed_poll_interval"].(float64))
	if feedPoll != 300 {
		t.Fatalf("expected feed_poll_interval=300 by default, got %d", feedPoll)
	}
}

func TestConsoleSettingsUpdateAndVerify(t *testing.T) {
	testutil.WaitForAPI(t)
	email := fmt.Sprintf("console-settings-update-%d@test.com", time.Now().UnixNano()%1_000_000)
	token, _, _ := testutil.LoginAndGetToken(t, email)

	// Set to non-defaults
	putResult := testutil.DoPut(t, "/api/v1/console/settings", map[string]interface{}{
		"recurring_publish":  false,
		"feed_poll_interval": 600,
	}, token)
	assertCode(t, putResult, 0)

	// Verify
	getResult := testutil.DoGet(t, "/api/v1/console/settings", token)
	assertCode(t, getResult, 0)

	data := getResult["data"].(map[string]interface{})
	if data["recurring_publish"].(bool) != false {
		t.Fatal("expected recurring_publish=false after update")
	}
	if int(data["feed_poll_interval"].(float64)) != 600 {
		t.Fatalf("expected feed_poll_interval=600, got %v", data["feed_poll_interval"])
	}
}

func TestConsoleSettingsUpdatePartial(t *testing.T) {
	testutil.WaitForAPI(t)
	email := fmt.Sprintf("console-settings-partial-%d@test.com", time.Now().UnixNano()%1_000_000)
	token, _, _ := testutil.LoginAndGetToken(t, email)

	// Get defaults first
	getResult1 := testutil.DoGet(t, "/api/v1/console/settings", token)
	assertCode(t, getResult1, 0)
	originalRP := getResult1["data"].(map[string]interface{})["recurring_publish"].(bool)

	// Update only feed_poll_interval
	putResult := testutil.DoPut(t, "/api/v1/console/settings", map[string]interface{}{
		"feed_poll_interval": 900,
	}, token)
	assertCode(t, putResult, 0)

	// Verify: feed_poll_interval changed, recurring_publish unchanged
	getResult2 := testutil.DoGet(t, "/api/v1/console/settings", token)
	assertCode(t, getResult2, 0)

	data := getResult2["data"].(map[string]interface{})
	if int(data["feed_poll_interval"].(float64)) != 900 {
		t.Fatalf("expected feed_poll_interval=900, got %v", data["feed_poll_interval"])
	}
	if data["recurring_publish"].(bool) != originalRP {
		t.Fatalf("recurring_publish changed unexpectedly: expected %v, got %v", originalRP, data["recurring_publish"])
	}
}

// ---------- Auth Code + Exchange ----------

func TestConsoleAuthCodeGeneration(t *testing.T) {
	testutil.WaitForAPI(t)
	token, _, _ := testutil.LoginAndGetToken(t, testEmail)

	result := testutil.DoPost(t, "/api/v1/console/auth-code", nil, token)
	assertCode(t, result, 0)

	data := result["data"].(map[string]interface{})
	code, ok := data["code"].(string)
	if !ok || code == "" {
		t.Fatal("expected non-empty code in response")
	}
	if !strings.HasPrefix(code, "cx_") {
		t.Fatalf("expected code to start with 'cx_', got %q", code)
	}
	if len(code) < 20 {
		t.Fatalf("code seems too short: %q", code)
	}
}

func TestConsoleExchangeRoundTrip(t *testing.T) {
	testutil.WaitForAPI(t)
	token, _, _ := testutil.LoginAndGetToken(t, testEmail)

	// Generate auth code
	authResult := testutil.DoPost(t, "/api/v1/console/auth-code", nil, token)
	assertCode(t, authResult, 0)
	code := authResult["data"].(map[string]interface{})["code"].(string)

	// Exchange code for token (WITHOUT auth header)
	exchangeResult := testutil.DoPost(t, "/api/v1/console/exchange", map[string]interface{}{
		"code": code,
	}, "")
	assertCode(t, exchangeResult, 0)

	exchangeData := exchangeResult["data"].(map[string]interface{})
	exchangedToken, ok := exchangeData["access_token"].(string)
	if !ok || exchangedToken == "" {
		t.Fatal("expected non-empty access_token from exchange")
	}

	// Verify the exchanged token works
	meResult := testutil.DoGet(t, "/api/v1/agents/me", exchangedToken)
	assertCode(t, meResult, 0)
}

func TestConsoleExchangeReplayProtection(t *testing.T) {
	testutil.WaitForAPI(t)
	token, _, _ := testutil.LoginAndGetToken(t, testEmail)

	// Generate auth code
	authResult := testutil.DoPost(t, "/api/v1/console/auth-code", nil, token)
	assertCode(t, authResult, 0)
	code := authResult["data"].(map[string]interface{})["code"].(string)

	// First exchange should succeed
	result1 := testutil.DoPost(t, "/api/v1/console/exchange", map[string]interface{}{
		"code": code,
	}, "")
	assertCode(t, result1, 0)

	// Second exchange with same code should fail (replay protection)
	result2 := testutil.DoPost(t, "/api/v1/console/exchange", map[string]interface{}{
		"code": code,
	}, "")
	code2 := int(result2["code"].(float64))
	if code2 == 0 {
		t.Fatal("expected error on replay (same code used twice), got code=0")
	}
}

func TestConsoleExchangeInvalidCode(t *testing.T) {
	testutil.WaitForAPI(t)

	result := testutil.DoPost(t, "/api/v1/console/exchange", map[string]interface{}{
		"code": "cx_invalid_nonexistent",
	}, "")
	code := int(result["code"].(float64))
	if code == 0 {
		t.Fatal("expected error for invalid code, got code=0")
	}
}

func TestConsoleExchangeNoAuthRequired(t *testing.T) {
	testutil.WaitForAPI(t)

	// POST to exchange without any Authorization header — should NOT get 401
	req, _ := http.NewRequest("POST", testutil.BaseURL+"/api/v1/console/exchange", strings.NewReader(`{"code":"cx_test"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == 401 {
		t.Fatal("exchange should not require auth, got 401")
	}
}

// ---------- PM Conversations Extension ----------

func TestListConversationsOriginTypeFilter(t *testing.T) {
	testutil.WaitForAPI(t)
	token, _, _ := testutil.LoginAndGetToken(t, testEmail)

	// With origin_type filter
	for _, originType := range []string{"item", "friend"} {
		t.Run(fmt.Sprintf("origin_type=%s", originType), func(t *testing.T) {
			result := testutil.DoGet(t, fmt.Sprintf("/api/v1/pm/conversations?origin_type=%s", originType), token)
			assertCode(t, result, 0)
			data := result["data"].(map[string]interface{})
			convs, ok := data["conversations"].([]interface{})
			if !ok {
				t.Fatal("expected conversations array")
			}
			t.Logf("origin_type=%s: %d conversations", originType, len(convs))
		})
	}

	// Without filter — should also succeed
	t.Run("no_filter", func(t *testing.T) {
		result := testutil.DoGet(t, "/api/v1/pm/conversations", token)
		assertCode(t, result, 0)
	})
}

// ---------- Assertion helpers ----------

func assertCode(t *testing.T, result map[string]interface{}, expected int) {
	t.Helper()
	code := int(result["code"].(float64))
	if code != expected {
		t.Fatalf("expected code=%d, got %d: %s", expected, code, result["msg"])
	}
}

func requireKey(t *testing.T, m map[string]interface{}, key string) {
	t.Helper()
	if _, ok := m[key]; !ok {
		t.Fatalf("missing required key %q in response", key)
	}
}
