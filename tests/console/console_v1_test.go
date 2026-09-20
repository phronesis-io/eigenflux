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
	suffix := time.Now().UnixNano()
	email := fmt.Sprintf("console-activity-hours-%d@test.com", suffix)
	token, agentID, _ := testutil.LoginAndGetToken(t, email)
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	now := time.Now()
	fixtures := []struct {
		logID     int64
		summary   string
		createdAt int64
	}{
		{suffix, "activity inside one-hour window", now.Add(-30 * time.Minute).UnixMilli()},
		{suffix + 1, "activity outside one-hour window", now.Add(-2 * time.Hour).UnixMilli()},
	}
	t.Cleanup(func() {
		if _, err := testutil.TestDB.Exec(`DELETE FROM agent_activity_log WHERE agent_id = $1 AND log_id IN ($2, $3)`, agentID, fixtures[0].logID, fixtures[1].logID); err != nil {
			t.Errorf("clean activity fixtures: %v", err)
		}
	})
	for _, fixture := range fixtures {
		if _, err := testutil.TestDB.Exec(`INSERT INTO agent_activity_log (log_id, agent_id, event_type, summary, detail, created_at)
			VALUES ($1, $2, 'feed_pull', $3, '{}', $4)`, fixture.logID, agentID, fixture.summary, fixture.createdAt); err != nil {
			t.Fatalf("insert activity fixture: %v", err)
		}
	}

	for _, hours := range []int{1, 3} {
		t.Run(fmt.Sprintf("hours=%d", hours), func(t *testing.T) {
			requestStartedAt := time.Now()
			result := testutil.DoGet(t, fmt.Sprintf("/api/v1/console/activity-log?hours=%d", hours), token)
			assertCode(t, result, 0)
			data := result["data"].(map[string]interface{})
			events, ok := data["events"].([]interface{})
			if !ok {
				t.Fatal("expected events array in response")
			}
			seen := map[string]int{}
			sinceMs := requestStartedAt.Add(-time.Duration(hours) * time.Hour).UnixMilli()
			for i, raw := range events {
				event := raw.(map[string]interface{})
				eventTime, ok := event["time"].(float64)
				if !ok {
					t.Fatalf("event[%d] missing numeric time: %v", i, event)
				}
				if eventTime < float64(sinceMs) {
					t.Fatalf("event[%d] time=%v is outside the %d-hour window", i, eventTime, hours)
				}
				for _, fixture := range fixtures {
					if event["summary"] == fixture.summary {
						seen[fixture.summary]++
						if eventTime != float64(fixture.createdAt) {
							t.Errorf("fixture %q time=%v, want %d", fixture.summary, eventTime, fixture.createdAt)
						}
					}
				}
			}
			for _, fixture := range fixtures {
				wantCount := 0
				if fixture.createdAt >= sinceMs {
					wantCount = 1
				}
				if got := seen[fixture.summary]; got != wantCount {
					t.Errorf("fixture %q appeared %d times in %d-hour window, want %d", fixture.summary, got, hours, wantCount)
				}
			}
		})
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
	email := fmt.Sprintf("console-highlights-%d@test.com", time.Now().UnixNano())
	token, agentID, _ := testutil.LoginAndGetToken(t, email)
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })
	readHighlights := func(t *testing.T) []interface{} {
		t.Helper()
		result := testutil.DoGet(t, "/api/v1/console/highlights?limit=5&lang=en", token)
		assertCode(t, result, 0)
		data := result["data"].(map[string]interface{})
		highlights, ok := data["highlights"].([]interface{})
		if !ok {
			t.Fatal("expected highlights array")
		}
		return highlights
	}
	t.Run("daily picks without deliveries", func(t *testing.T) {
		highlights := readHighlights(t)
		if len(highlights) != 3 {
			t.Fatalf("expected three daily picks, got %d", len(highlights))
		}
		for _, h := range highlights {
			hl := h.(map[string]interface{})
			if hl["global_pick"] != true || hl["source"] != "EigenFlux" {
				t.Fatalf("expected global daily pick: %v", hl)
			}
			for _, key := range []string{"content", "summary"} {
				if value, _ := hl[key].(string); value == "" {
					t.Fatalf("daily pick missing %s: %v", key, hl)
				}
			}
		}
	})
	t.Run("delivered broadcast", func(t *testing.T) {
		authorEmail := fmt.Sprintf("console-highlight-author-%d@test.com", time.Now().UnixNano())
		_, authorID, _ := testutil.LoginAndGetToken(t, authorEmail)
		t.Cleanup(func() { testutil.CleanupTestEmails(t, authorEmail) })
		itemID := time.Now().UnixNano()
		now := time.Now().UnixMilli()
		impressionID := fmt.Sprintf("console-highlight-%d", itemID)
		t.Cleanup(func() {
			testutil.TestDB.Exec("DELETE FROM replay_logs WHERE item_id = $1", itemID)
			testutil.TestDB.Exec("DELETE FROM processed_items WHERE item_id = $1", itemID)
			testutil.TestDB.Exec("DELETE FROM raw_items WHERE item_id = $1", itemID)
		})
		if _, err := testutil.TestDB.Exec(`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES ($1, $2, 'Delivered highlight fixture', $3)`, itemID, authorID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := testutil.TestDB.Exec(`INSERT INTO processed_items (item_id, status, broadcast_type, summary, updated_at) VALUES ($1, 3, 'info', 'Delivered summary', $2)`, itemID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := testutil.TestDB.Exec(`INSERT INTO replay_logs (id, impression_id, agent_id, item_id, item_score, served_at, created_at, delivered) VALUES ($1,$2,$3,$1,1,$4,$4,true)`, itemID, impressionID, agentID, now); err != nil {
			t.Fatal(err)
		}
		highlights := readHighlights(t)
		if len(highlights) != 1 {
			t.Fatalf("expected one delivered broadcast, got %d", len(highlights))
		}
		hl := highlights[0].(map[string]interface{})
		if testutil.MustID(t, hl["item_id"], "item_id") != itemID || hl["impression_id"] != impressionID {
			t.Fatalf("highlight must preserve delivery identity: %v", hl)
		}
		if hl["summary"] != "Delivered summary" || hl["global_pick"] == true {
			t.Fatalf("expected the delivered broadcast content: %v", hl)
		}
	})
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
	for _, tc := range []struct {
		name     string
		age      time.Duration
		override int
		want     int
	}{
		{name: "new_agent", want: 3600},
		{name: "after_three_days", age: 4 * 24 * time.Hour, want: 300},
		{name: "new_agent_explicit_setting", override: 900, want: 900},
		{name: "after_three_days_explicit_setting", age: 4 * 24 * time.Hour, override: 900, want: 900},
	} {
		t.Run(tc.name, func(t *testing.T) {
			email := fmt.Sprintf("console-settings-defaults-%d@test.com", time.Now().UnixNano())
			token, agentID, _ := testutil.LoginAndGetToken(t, email)
			t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })
			t.Cleanup(func() {
				if _, err := testutil.TestDB.Exec(`DELETE FROM agent_settings WHERE agent_id = $1`, agentID); err != nil {
					t.Errorf("clean settings fixture: %v", err)
				}
			})

			// Materialize the settings row before setting up registration age.
			initial := testutil.DoGet(t, "/api/v1/console/settings", token)
			assertCode(t, initial, 0)
			if tc.age > 0 {
				createdAt := time.Now().Add(-tc.age).UnixMilli()
				tx, err := testutil.TestDB.Begin()
				if err != nil {
					t.Fatalf("begin settings age fixture: %v", err)
				}
				defer tx.Rollback()
				// Keep the profile timestamp and its settings cache consistent.
				for _, query := range []string{
					`UPDATE agents SET created_at = $1 WHERE agent_id = $2`,
					`UPDATE agent_settings SET agent_created_at_ms = $1 WHERE agent_id = $2`,
				} {
					result, err := tx.Exec(query, createdAt, agentID)
					if err != nil {
						t.Fatalf("set registration age: %v", err)
					}
					if rows, err := result.RowsAffected(); err != nil || rows != 1 {
						t.Fatalf("set registration age affected %d rows, want 1: %v", rows, err)
					}
				}
				if err := tx.Commit(); err != nil {
					t.Fatalf("commit registration age fixture: %v", err)
				}
			}
			if tc.override > 0 {
				result := testutil.DoPut(t, "/api/v1/console/settings", map[string]interface{}{
					"feed_poll_interval": tc.override,
				}, token)
				assertCode(t, result, 0)
			}

			result := testutil.DoGet(t, "/api/v1/console/settings", token)
			assertCode(t, result, 0)
			data := result["data"].(map[string]interface{})
			if recurringPublish, ok := data["recurring_publish"].(bool); !ok || !recurringPublish {
				t.Fatalf("expected recurring_publish=true by default, got %v", data["recurring_publish"])
			}
			if feedPoll, ok := data["feed_poll_interval"].(float64); !ok || feedPoll != float64(tc.want) {
				t.Fatalf("expected feed_poll_interval=%d, got %v", tc.want, data["feed_poll_interval"])
			}
		})
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

func TestConsoleSettingsLang(t *testing.T) {
	testutil.WaitForAPI(t)
	email := fmt.Sprintf("console-settings-lang-%d@test.com", time.Now().UnixNano()%1_000_000)
	token, _, _ := testutil.LoginAndGetToken(t, email)

	// Default: never set.
	getResult := testutil.DoGet(t, "/api/v1/console/settings", token)
	assertCode(t, getResult, 0)
	if lang := getResult["data"].(map[string]interface{})["lang"].(string); lang != "" {
		t.Fatalf("expected empty lang by default, got %q", lang)
	}

	// Dashboard mirrors its display language.
	putResult := testutil.DoPut(t, "/api/v1/console/settings", map[string]interface{}{
		"lang": "zh",
	}, token)
	assertCode(t, putResult, 0)

	getResult = testutil.DoGet(t, "/api/v1/console/settings", token)
	assertCode(t, getResult, 0)
	if lang := getResult["data"].(map[string]interface{})["lang"].(string); lang != "zh" {
		t.Fatalf("expected lang=zh after update, got %q", lang)
	}

	// Only ""/"zh"/"en" are accepted.
	badResult := testutil.DoPut(t, "/api/v1/console/settings", map[string]interface{}{
		"lang": "fr",
	}, token)
	if code := int(badResult["code"].(float64)); code == 0 {
		t.Fatal("expected non-zero code for invalid lang")
	}
	getResult = testutil.DoGet(t, "/api/v1/console/settings", token)
	if lang := getResult["data"].(map[string]interface{})["lang"].(string); lang != "zh" {
		t.Fatalf("invalid lang must not overwrite, expected zh, got %q", lang)
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

	// With origin_type filter. "unbroken" surfaces inbound non-friend DMs still
	// waiting for the user's first reply (the dashboard "non-friend" tab).
	for _, originType := range []string{"item", "friend", "unbroken"} {
		t.Run(fmt.Sprintf("origin_type=%s", originType), func(t *testing.T) {
			result := testutil.DoGet(t, fmt.Sprintf("/api/v1/pm/conversations?origin_type=%s", originType), token)
			assertCode(t, result, 0)
			data := result["data"].(map[string]interface{})
			convs, ok := data["conversations"].([]interface{})
			if !ok {
				t.Fatal("expected conversations array")
			}
			// Unbroken conversations are inbound first-contact DMs: each must have
			// exactly one message (not yet ice-broken) and a non-friend origin.
			if originType == "unbroken" {
				for _, c := range convs {
					m := c.(map[string]interface{})
					if mc, ok := m["msg_count"].(float64); ok && int(mc) != 1 {
						t.Errorf("unbroken conv %v has msg_count=%v, want 1", m["conv_id"], mc)
					}
					if ot, ok := m["origin_type"].(string); ok && ot == "friend" {
						t.Errorf("unbroken conv %v has origin_type=friend, want non-friend", m["conv_id"])
					}
				}
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
