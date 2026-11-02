package consolev2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	redis "github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/config"
)

func TestHomeHTTPContracts(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for homepage PostgreSQL contracts")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UnixMilli()
	agentIDValue := time.Now().UnixNano()
	email := fmt.Sprintf("home-contract-%d@example.test", agentIDValue)
	shortID, err := agentidentity.GenerateShortID()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO agents
		(agent_id, short_id, email, email_kind, agent_name, bio, created_at, updated_at, identity_state)
		VALUES (?, ?, ?, 'v2_bound', 'Home Contract Agent', '', ?, ?, 'active')`,
		agentIDValue, shortID, email, now, now).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM agents WHERE agent_id = ?`, agentIDValue).Error })

	var principalID int64
	if err := db.Raw(`INSERT INTO agent_principals
		(agent_id, key_type, key_fingerprint, public_key, key_version, status, created_at, last_seen_at)
		VALUES (?, 'ed25519-v1', ?, decode(repeat('42', 32), 'hex'), 1, 'active', ?, ?)
		RETURNING principal_id`, agentIDValue, fmt.Sprintf("home-contract-%d", agentIDValue), now, now).
		Scan(&principalID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO agent_cards
		(agent_id, public_card, private_card, schema_version, source_version, rebuild_fence,
		 card_version, public_card_version, generated_at, public_card_generated_at)
		VALUES (?, '{}'::jsonb, '{"timezone":"UTC"}'::jsonb, 1, 1, 0, 1, 1, ?, 0)`,
		agentIDValue, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO agent_onboarding_v2
		(agent_id, state, current_step, revision, created_at, updated_at)
		VALUES (?, 'in_progress', 4, 1, ?, ?)`, agentIDValue, now, now).Error; err != nil {
		t.Fatal(err)
	}
	sessionID := fmt.Sprintf("home-contract-session-%d", agentIDValue)
	sessionSecret := fmt.Sprintf("home-contract-secret-%d", agentIDValue)
	if err := db.Exec(`INSERT INTO console_v2_sessions
		(session_id, session_secret_hash, agent_id, principal_id, csrf_secret_hash, status, scopes,
		 issued_at, idle_expires_at, absolute_expires_at, last_seen_at, auth_method)
		VALUES (?, ?, ?, ?, ?, 'active', '{}'::text[], ?, ?, ?, ?, 'handoff')`,
		sessionID, hashString(sessionSecret), agentIDValue, principalID, hashString("unused-csrf"),
		now, now+int64(time.Hour/time.Millisecond), now+int64(2*time.Hour/time.Millisecond), now).Error; err != nil {
		t.Fatal(err)
	}

	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	svc, err := NewService(db, &fixedIDGenerator{id: agentIDValue + 100}, &config.Config{
		EnableCommunicationV2:    true,
		ConsoleV2BootstrapSecret: "home-contract-secret",
		ConsoleV2OTPPepper:       "home-contract-pepper",
		ConsoleV2PublicURL:       "https://console.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.redisClient = redisClient
	svc.enableAttentionV1 = true
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	svc.Register(h)

	paths := []string{
		"/api/v2/console/home/discovery",
		"/api/v2/console/home/activity",
		"/api/v2/console/home/worth-watching",
	}
	for _, path := range paths {
		t.Run("unauthorized "+path, func(t *testing.T) {
			status, payload, _ := performJSON(t, h, http.MethodGet, path, map[string]interface{}{})
			if status != http.StatusUnauthorized || responseErrorCode(t, payload) != "CONSOLE_SESSION_REQUIRED" {
				t.Fatalf("status=%d payload=%#v", status, payload)
			}
		})
	}

	cookie := ut.Header{Key: "Cookie", Value: consoleCookieName + "=" + sessionID + "." + sessionSecret}
	for _, path := range paths {
		t.Run("onboarding required "+path, func(t *testing.T) {
			status, payload, _ := performJSON(t, h, http.MethodGet, path, map[string]interface{}{}, cookie)
			if status != http.StatusConflict || responseErrorCode(t, payload) != "ONBOARDING_REQUIRED" {
				t.Fatalf("status=%d payload=%#v", status, payload)
			}
		})
	}

	if err := db.Exec(`INSERT INTO agent_context_revisions
		(agent_id, revision, compiled_context, schema_version, generated_at)
		VALUES (?, 1, '{}'::jsonb, 1, ?)`, agentIDValue, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE agent_onboarding_v2
		SET state='completed', current_step=5, active_context_revision=1, completed_at=?, updated_at=?
		WHERE agent_id=?`, now, now, agentIDValue).Error; err != nil {
		t.Fatal(err)
	}
	demandAgentID := agentIDValue + 1
	publishAgentID := agentIDValue + 2
	demandShortID, err := agentidentity.GenerateShortID()
	if err != nil {
		t.Fatal(err)
	}
	publishShortID, err := agentidentity.GenerateShortID()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO agents
		(agent_id, short_id, email, email_kind, agent_name, bio, created_at, updated_at, identity_state)
		VALUES (?, ?, ?, 'internal_alias', 'Demand Contract Agent', '', ?, ?, 'active'),
		       (?, ?, ?, 'internal_alias', 'Publish Contract Agent', '', ?, ?, 'active')`,
		demandAgentID, demandShortID, fmt.Sprintf("home-demand-%d@agent.eigenflux.internal", demandAgentID), now-int64(30*24*time.Hour/time.Millisecond), now,
		publishAgentID, publishShortID, fmt.Sprintf("home-publish-%d@agent.eigenflux.internal", publishAgentID), now-int64(30*24*time.Hour/time.Millisecond), now).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM agents WHERE agent_id IN (?, ?)`, demandAgentID, publishAgentID).Error
	})

	firstVoiceItemID := agentIDValue + 10
	oldDemandItemID := agentIDValue + 11
	newDemandItemID := agentIDValue + 12
	newPublishItemID := agentIDValue + 13
	ineligibleItemID := agentIDValue + 14
	if err := db.Exec(`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES
		(?, ?, 'new Agent first voice', ?), (?, ?, 'older demand', ?),
		(?, ?, 'newer demand', ?), (?, ?, 'newest general publish', ?),
		(?, ?, 'homepage-ineligible content', ?)`,
		firstVoiceItemID, agentIDValue, now-4000,
		oldDemandItemID, demandAgentID, now-3000,
		newDemandItemID, demandAgentID, now-2000,
		newPublishItemID, publishAgentID, now-1000,
		ineligibleItemID, agentIDValue, now-500).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO processed_items
		(item_id, status, summary, broadcast_type, quality_score, lang, updated_at,
		 homepage_eligible, homepage_evaluation_version, homepage_real_world_relevant)
		VALUES (?, 3, 'new Agent first voice', 'info', 0.70, 'en', ?, TRUE, ?, FALSE),
		       (?, 3, 'older demand', 'demand', 0.95, 'en', ?, TRUE, ?, FALSE),
		       (?, 3, 'newer demand', 'demand', 0.80, 'en', ?, TRUE, ?, FALSE),
		       (?, 3, 'newest general publish', 'info', 0.99, 'en', ?, TRUE, ?, FALSE),
		       (?, 3, 'homepage-ineligible content', 'info', 0.99, 'en', ?, FALSE, ?, FALSE)`,
		firstVoiceItemID, now, homepageEvaluationVersion,
		oldDemandItemID, now, homepageEvaluationVersion,
		newDemandItemID, now, homepageEvaluationVersion,
		newPublishItemID, now, homepageEvaluationVersion,
		ineligibleItemID, now, homepageEvaluationVersion).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO item_stats
		(item_id, author_agent_id, score_1_count, score_2_count, created_at, updated_at)
		VALUES (?, ?, 1, 0, ?, ?), (?, ?, 9, 0, ?, ?),
		       (?, ?, 2, 0, ?, ?), (?, ?, 3, 0, ?, ?)`,
		firstVoiceItemID, agentIDValue, now, now,
		oldDemandItemID, demandAgentID, now, now,
		newDemandItemID, demandAgentID, now, now,
		newPublishItemID, publishAgentID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM processed_items WHERE item_id IN (?, ?, ?, ?, ?)`, firstVoiceItemID, oldDemandItemID, newDemandItemID, newPublishItemID, ineligibleItemID).Error
		_ = db.Exec(`DELETE FROM raw_items WHERE item_id IN (?, ?, ?, ?, ?)`, firstVoiceItemID, oldDemandItemID, newDemandItemID, newPublishItemID, ineligibleItemID).Error
	})

	t.Run("official contacts remain first across pages", func(t *testing.T) {
		testOfficialContactsPagination(t, db, svc, h, cookie, agentIDValue, demandAgentID, publishAgentID, now)
	})
	t.Run("country sources agree across Console", func(t *testing.T) {
		testConsoleCountrySources(t, db, svc, h, cookie, agentIDValue, demandAgentID, publishAgentID, firstVoiceItemID, newPublishItemID, now)
	})

	t.Run("activity eligibility applies to broadcasts and replies", func(t *testing.T) {
		cases := []struct {
			name     string
			eligible interface{}
			version  string
			status   int
			visible  bool
		}{
			{"current eligible", true, homepageEvaluationVersion, 3, true},
			{"current ineligible", false, homepageEvaluationVersion, 3, false},
			{"old evaluation", true, "homepage-v1", 3, false},
			{"missing evaluation", true, "", 3, false},
			{"missing eligibility", nil, homepageEvaluationVersion, 3, false},
			{"discarded", true, homepageEvaluationVersion, 4, false},
			{"retracted", true, homepageEvaluationVersion, 5, false},
		}
		for index, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				tx := db.Begin()
				if tx.Error != nil {
					t.Fatal(tx.Error)
				}
				defer tx.Rollback()
				originalDB := svc.db
				svc.db = tx
				defer func() { svc.db = originalDB }()
				exec := func(query string, args ...interface{}) {
					t.Helper()
					if err := tx.Exec(query, args...).Error; err != nil {
						t.Fatal(err)
					}
				}
				itemID, convID, msgID := agentIDValue+1000+int64(index), agentIDValue+2000+int64(index), agentIDValue+3000+int64(index)
				exec(`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at)
					VALUES (?, ?, 'activity eligibility contract', ?)`, itemID, agentIDValue, now)
				exec(`INSERT INTO processed_items (item_id, status, summary, broadcast_type, quality_score, lang, updated_at,
					homepage_eligible, homepage_evaluation_version, homepage_real_world_relevant)
					VALUES (?, ?, 'activity eligibility contract', 'info', 0.9, 'en', ?, ?, ?, FALSE)`,
					itemID, tc.status, now, tc.eligible, tc.version)
				exec(`INSERT INTO conversations (conv_id, participant_a, participant_b, initiator_id, last_sender_id,
					origin_type, origin_id, updated_at) VALUES (?, ?, ?, ?, ?, 'broadcast', ?, ?)`,
					convID, agentIDValue, demandAgentID, demandAgentID, demandAgentID, itemID, now)
				exec(`INSERT INTO private_messages (msg_id, conv_id, sender_id, receiver_id, content, created_at)
					VALUES (?, ?, ?, ?, 'reply content must stay private', ?)`, msgID, convID, demandAgentID, agentIDValue, now)

				activity, err := svc.loadHomeActivity(context.Background(), now)
				if err != nil {
					t.Fatal(err)
				}
				seen := map[string]bool{}
				for _, event := range activity.Events {
					if event.BroadcastID != strconv.FormatInt(itemID, 10) {
						continue
					}
					seen[event.ID] = true
					if !tc.visible {
						t.Fatalf("ineligible broadcast exposed through %s: %#v", event.Type, event)
					}
					if event.BroadcastContent != "activity eligibility contract" {
						t.Fatalf("unexpected public broadcast content: %#v", event)
					}
				}
				for _, eventID := range []string{"broadcast:" + strconv.FormatInt(itemID, 10), "reply:" + strconv.FormatInt(msgID, 10)} {
					if seen[eventID] != tc.visible {
						t.Errorf("event %s visible=%v, want %v", eventID, seen[eventID], tc.visible)
					}
				}
			})
		}
	})

	generatedAt := time.Now().UnixMilli()
	dayStart := homeDiscoveryDayStart(time.Now(), time.UTC)
	discoveryCached := homeDiscoveryResponse{
		Items: []homeDiscoveryAgent{{RuleKey: "recognized", AgentID: "101", ShortID: "agent-101", AgentName: "Cached Discovery", JoinedAt: now,
			Metric: homeDiscoveryMetric{Key: "recognition", Value: 3}}},
		WindowStart: dayStart, WindowTimezone: "UTC", GeneratedAt: generatedAt,
		CacheTTLSeconds: int64(homeDiscoveryCacheTTL / time.Second),
	}
	activityCached := homeActivityResponse{
		Events:      []homeActivityEvent{{ID: "broadcast:201", Type: "broadcast", CreatedAt: now, ActorName: "Cached Activity", Private: false}},
		GeneratedAt: generatedAt, CacheTTLSeconds: int64(homeActivityCacheTTL / time.Second),
	}
	worthCached := homeWorthWatchingResponse{
		Items: []homeWorthWatchingItem{{RuleKey: "trending", ItemID: "301", Content: "cached worth watching", CreatedAt: now,
			AgentID: "101", AgentShortID: "agent-101", AgentName: "Cached Worth", Metric: homeWorthWatchingMetric{Key: "engagement", Value: 5}}},
		WindowStart: time.Now().Add(-7 * 24 * time.Hour).UnixMilli(), WindowTimezone: "UTC", GeneratedAt: generatedAt,
		CacheTTLSeconds: int64(homeWorthWatchingCacheTTL / time.Second), EvaluationVersion: homepageEvaluationVersion,
	}
	writeJSON := func(key string, value interface{}) {
		t.Helper()
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if setErr := redisClient.Set(context.Background(), key, raw, time.Minute).Err(); setErr != nil {
			t.Fatal(setErr)
		}
	}
	discoveryKey := homeDiscoveryCacheKey("UTC", dayStart)
	worthKey := homeWorthWatchingCacheKey()
	writeJSON(discoveryKey, discoveryCached)
	writeJSON(homeActivityCacheKey, activityCached)
	writeJSON(worthKey, worthCached)

	t.Run("cache hit response envelopes and field types", func(t *testing.T) {
		assertHomeCachedResponse(t, h, "/api/v2/console/home/discovery", "items", "agent_id", "101", cookie)
		assertHomeCachedResponse(t, h, "/api/v2/console/home/activity", "events", "id", "broadcast:201", cookie)
		assertHomeCachedResponse(t, h, "/api/v2/console/home/worth-watching", "items", "item_id", "301", cookie)
	})

	t.Run("corrupt cache falls back to SQL and rebuilds bounded responses", func(t *testing.T) {
		for _, key := range []string{discoveryKey, homeActivityCacheKey, worthKey} {
			if err := redisClient.Set(context.Background(), key, "{", time.Minute).Err(); err != nil {
				t.Fatal(err)
			}
		}
		limits := map[string]int{
			"/api/v2/console/home/discovery":      homeDiscoveryResultLimit,
			"/api/v2/console/home/activity":       homeActivityLimit,
			"/api/v2/console/home/worth-watching": 7 * homeWorthWatchingCandidateMax,
		}
		for _, path := range paths {
			status, payload, _ := performJSON(t, h, http.MethodGet, path, map[string]interface{}{}, cookie)
			if status != http.StatusOK {
				t.Fatalf("%s status=%d payload=%#v", path, status, payload)
			}
			data := responseData(t, payload)
			collection := "items"
			if path == "/api/v2/console/home/activity" {
				collection = "events"
			}
			rows, ok := data[collection].([]interface{})
			if !ok || len(rows) > limits[path] {
				t.Fatalf("%s %s=%#v limit=%d", path, collection, data[collection], limits[path])
			}
			if path == "/api/v2/console/home/worth-watching" {
				assertHomeWorthWatchingReasons(t, rows, map[string]string{
					"new_real_world_demand":  strconv.FormatInt(newDemandItemID, 10),
					"noteworthy_new_publish": strconv.FormatInt(newPublishItemID, 10),
					"new_agent_first_voice":  strconv.FormatInt(firstVoiceItemID, 10),
				})
			}
			if path == "/api/v2/console/home/activity" {
				assertHomeActivityExcludesBroadcast(t, rows, ineligibleItemID)
			}
		}
		for _, key := range []string{discoveryKey, homeActivityCacheKey, worthKey} {
			raw, err := redisClient.Get(context.Background(), key).Bytes()
			if err != nil || !json.Valid(raw) {
				t.Fatalf("cache %s was not rebuilt: err=%v raw=%q", key, err, raw)
			}
		}
	})

	t.Run("redis outage degrades to SQL", func(t *testing.T) {
		available := svc.redisClient
		unavailable := redis.NewClient(&redis.Options{
			Addr: "127.0.0.1:1", MaxRetries: 0,
			DialTimeout: 10 * time.Millisecond, ReadTimeout: 10 * time.Millisecond, WriteTimeout: 10 * time.Millisecond,
		})
		svc.redisClient = unavailable
		t.Cleanup(func() {
			svc.redisClient = available
			_ = unavailable.Close()
		})
		for _, path := range paths {
			status, payload, _ := performJSON(t, h, http.MethodGet, path, map[string]interface{}{}, cookie)
			if status != http.StatusOK {
				t.Fatalf("%s status=%d payload=%#v", path, status, payload)
			}
		}
	})
}

func assertHomeActivityExcludesBroadcast(t *testing.T, rows []interface{}, itemID int64) {
	t.Helper()
	wantID := "broadcast:" + strconv.FormatInt(itemID, 10)
	for _, raw := range rows {
		row, ok := raw.(map[string]interface{})
		if ok && row["id"] == wantID {
			t.Fatalf("homepage-ineligible broadcast appeared in home activity: %#v", row)
		}
	}
}

func assertHomeWorthWatchingReasons(t *testing.T, rows []interface{}, want map[string]string) {
	t.Helper()
	got := make(map[string]string, len(want))
	for _, raw := range rows {
		row, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		rule, _ := row["rule_key"].(string)
		itemID, _ := row["item_id"].(string)
		if _, tracked := want[rule]; tracked {
			if _, exists := got[rule]; !exists {
				got[rule] = itemID
			}
		}
	}
	for rule, itemID := range want {
		if got[rule] != itemID {
			t.Fatalf("worth-watching rule %s item=%q, want %q; all=%#v", rule, got[rule], itemID, rows)
		}
	}
}

func assertHomeCachedResponse(t *testing.T, h *server.Hertz, path, collection, idField, wantID string, cookie ut.Header) {
	t.Helper()
	status, payload, _ := performJSON(t, h, http.MethodGet, path, map[string]interface{}{}, cookie)
	if status != http.StatusOK {
		t.Fatalf("%s status=%d payload=%#v", path, status, payload)
	}
	data := responseData(t, payload)
	rows, ok := data[collection].([]interface{})
	if !ok || len(rows) != 1 {
		t.Fatalf("%s %s=%#v", path, collection, data[collection])
	}
	row, ok := rows[0].(map[string]interface{})
	if !ok || row[idField] != wantID {
		t.Fatalf("%s row=%#v, want %s=%q", path, rows[0], idField, wantID)
	}
	if _, ok := data["generated_at"].(float64); !ok {
		t.Fatalf("%s generated_at has unexpected type: %#v", path, data["generated_at"])
	}
	if _, ok := data["cache_ttl_seconds"].(float64); !ok {
		t.Fatalf("%s cache_ttl_seconds has unexpected type: %#v", path, data["cache_ttl_seconds"])
	}
}

func testConsoleCountrySources(t *testing.T, db *gorm.DB, svc *Service, h *server.Hertz, cookie ut.Header,
	actorID, peerID, missingCardID, itemID, missingCardItemID, now int64) {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	originalDB := svc.db
	svc.db = tx
	t.Cleanup(func() { svc.db = originalDB; tx.Rollback() })
	exec := func(sql string, args ...interface{}) {
		t.Helper()
		if err := tx.Exec(sql, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE agents SET agent_name_en='James Research' WHERE agent_id=?`, actorID)
	exec(`UPDATE agents SET agent_name_en='Liu Assistant' WHERE agent_id=?`, peerID)
	// The canonical card deliberately disagrees with both legacy profile fields.
	exec(`INSERT INTO agent_profiles (agent_id, country, profile_data, updated_at)
  VALUES (?, '', '{"geo":"US"}', ?), (?, 'US', '{"geo":"US"}', ?)`, actorID, now, missingCardID, now)
	exec(`UPDATE agent_cards SET private_card='{"geo":" CN ","timezone":"UTC"}',
  public_card_version=2, public_card_generated_at=? WHERE agent_id=?`, now, actorID)
	// A card-only peer proves that no legacy profile row is required.
	exec(`INSERT INTO agent_cards (agent_id, public_card, private_card, schema_version, source_version,
  rebuild_fence, card_version, public_card_version, generated_at, public_card_generated_at)
  VALUES (?, '{}', '{"geo":"Singapore"}', 1, 1, 0, 1, 1, ?, 0)`, peerID, now)
	convID, replyConvID := actorID+30, actorID+31
	exec(`INSERT INTO conversations (conv_id, participant_a, participant_b, initiator_id, last_sender_id,
  origin_type, origin_id, updated_at) VALUES (?, ?, ?, ?, ?, 'direct', 0, ?), (?, ?, ?, ?, ?, 'broadcast', ?, ?)`,
		convID, actorID, peerID, actorID, actorID, now, replyConvID, actorID, peerID, actorID, actorID, itemID, now)
	exec(`INSERT INTO private_messages (msg_id, conv_id, sender_id, receiver_id, content, created_at)
  VALUES (?, ?, ?, ?, 'direct message', ?), (?, ?, ?, ?, 'broadcast reply', ?)`,
		actorID+32, convID, actorID, peerID, now, actorID+33, replyConvID, actorID, peerID, now)
	exec(`INSERT INTO user_relations (from_uid, to_uid, rel_type, created_at) VALUES (?, ?, 1, ?)`, actorID, peerID, now)
	exec(`INSERT INTO agent_commands (agent_id, command_type, payload_hash, idempotency_key, created_at)
  VALUES (?, 'task_delegation', 'country-test', 'country-test', ?)`, actorID, now)
	activity, err := svc.loadHomeActivity(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range activity.Events {
		if event.ActorName != "Home Contract Agent" && event.ActorName != "H***" {
			continue
		}
		seen[event.Type] = true
		if event.Private {
			if event.ActorNameEn != "J***" || event.ActorShortID != "" {
				t.Fatalf("private identity is not masked: %#v", event)
			}
			if (event.Type == "message" || event.Type == "relation") && event.CounterpartNameEn != "L***" {
				t.Fatalf("private counterpart English mask = %q", event.CounterpartNameEn)
			}
		} else if event.ActorNameEn != "James Research" {
			t.Fatalf("public English name = %q", event.ActorNameEn)
		}
		if event.ActorCountryCode != "CN" {
			t.Fatalf("%s actor country=%q", event.Type, event.ActorCountryCode)
		}
		if (event.Type == "relation" || event.Type == "message") && event.CounterpartCountry != "SG" {
			t.Fatalf("%s counterpart country=%q", event.Type, event.CounterpartCountry)
		}
	}
	for _, kind := range []string{"broadcast", "profile", "relation", "message", "reply", "delegation"} {
		if !seen[kind] {
			t.Fatalf("missing activity type %s", kind)
		}
	}
	selected := []homeWorthWatchingRule{
		{Key: "test", Rows: []homeWorthWatchingCandidate{{ItemID: itemID}}},
		{Key: "missing", Rows: []homeWorthWatchingCandidate{{ItemID: missingCardItemID}}},
	}
	worth, err := svc.hydrateHomeWorthWatching(context.Background(), selected)
	if err != nil {
		t.Fatal(err)
	}
	if len(worth) != 2 || worth[0].AgentCountryCode != "CN" || worth[1].AgentCountryCode != "" {
		t.Fatalf("worth watching countries=%#v", worth)
	}
	// Exercise the homepage SQL projection on a historical PGC record.
	const title = "Election ‘war rooms’ enter the AI age"
	const ts = "2026-11-02T09:45:37+00:00"
	exec("UPDATE agents SET email=? WHERE agent_id=?", fmt.Sprintf("grace-%d@pgc.example.test", actorID), actorID)
	oldSuffixes := svc.pgcEmailSuffixes
	svc.pgcEmailSuffixes = []string{"@pgc.example.test"}
	defer func() { svc.pgcEmailSuffixes = oldSuffixes }()
	exec("UPDATE raw_items SET raw_content=? WHERE item_id=?", "Source title: "+title+"\nSource timestamp (UTC): "+ts+"\n\nBoth companies confirmed.", itemID)
	projected, err := svc.hydrateHomeWorthWatching(context.Background(), selected[:1])
	if err != nil || len(projected) != 1 || projected[0].SourceTitle != title || projected[0].SourceTimestamp != ts || projected[0].Content != "Both companies confirmed." {
		t.Fatalf("source projection: %+v, %v", projected, err)
	}
	status, payload, _ := performJSON(t, h, http.MethodGet, "/api/v2/console/today", map[string]interface{}{}, cookie)
	if status != http.StatusOK {
		t.Fatalf("Today status=%d payload=%#v", status, payload)
	}
	data := responseData(t, payload)
	encounters, ok := data["encounters"].([]interface{})
	if !ok || len(encounters) != 1 || encounters[0].(map[string]interface{})["country_code"] != "SG" {
		t.Fatalf("Today encounters=%#v", data["encounters"])
	}
	if encounters[0].(map[string]interface{})["show_add_friend"] != true {
		t.Fatalf("Today encounters should default to allowing friend requests: %#v", encounters)
	}
	for _, visible := range []bool{false, true} {
		exec(`INSERT INTO agent_settings (agent_id, show_add_friend, updated_at) VALUES (?, ?, ?)
			ON CONFLICT (agent_id) DO UPDATE SET show_add_friend = EXCLUDED.show_add_friend,
			updated_at = EXCLUDED.updated_at`, peerID, visible, time.Now().UnixMilli())
		settingStatus, settingPayload, _ := performJSON(t, h, http.MethodGet, "/api/v2/console/today", map[string]interface{}{}, cookie)
		if settingStatus != http.StatusOK {
			t.Fatalf("Today setting status=%d payload=%#v", settingStatus, settingPayload)
		}
		settingEncounters := responseData(t, settingPayload)["encounters"].([]interface{})
		if len(settingEncounters) != 1 || settingEncounters[0].(map[string]interface{})["show_add_friend"] != visible {
			t.Fatalf("Today encounters should respect show_add_friend=%v: %#v", visible, settingEncounters)
		}
	}
	contexts := data["agent_contexts"].(map[string]interface{})
	if contexts[strconv.FormatInt(peerID, 10)].(map[string]interface{})["country_code"] != "SG" {
		t.Fatalf("Today contexts=%#v", contexts)
	}

	exec(`INSERT INTO agent_feed_exposures (agent_id, source_type, source_id, content_class,
  author_agent_id, first_seen_at, last_seen_at) VALUES (?, 'broadcast', ?, 'ugc', ?, ?, ?)`, actorID, itemID, actorID, now, now)
	attentionID := actorID + 40
	exec(`INSERT INTO agent_attention_items (attention_id, agent_id, title, source_type, source_id,
  created_at, expires_at, producer, protocol_version, client_item_id, payload_hash, source_ref,
  actions_snapshot, generated_at, updated_at)
  VALUES (?, ?, 'country source', 'broadcast', ?, ?, ?, 'agent', 'agent_attention.v1', 'country-source',
  'country-source', jsonb_build_object('type', 'broadcast', 'id', ?::text), '[{"action_key":"open"}]', ?, ?)`,
		attentionID, actorID, itemID, now, now+3600000, strconv.FormatInt(itemID, 10), now, now)
	status, sourcePayload, _ := performJSON(t, h, http.MethodGet,
		fmt.Sprintf("/api/v2/console/attention-items/%d/source", attentionID), map[string]interface{}{}, cookie)
	if status != http.StatusOK {
		t.Fatalf("source status=%d payload=%#v", status, sourcePayload)
	}
	if responseData(t, sourcePayload)["detail"].(map[string]interface{})["country_code"] != "CN" {
		t.Fatalf("broadcast source country=%#v", sourcePayload)
	}
	// Clearing the canonical value must not resurrect an obsolete country.
	exec(`UPDATE agent_cards SET private_card='{}' WHERE agent_id=?`, actorID)
	worth, err = svc.hydrateHomeWorthWatching(context.Background(), selected[:1])
	if err != nil || len(worth) != 1 || worth[0].AgentCountryCode != "" {
		t.Fatalf("cleared country: items=%#v err=%v", worth, err)
	}
	exec(`INSERT INTO user_relations (from_uid, to_uid, rel_type, created_at) VALUES (?, ?, 2, ?)`, actorID, peerID, now)
	blocked, err := svc.loadCommunicationContexts(actorID, []int64{peerID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if blocked[strconv.FormatInt(peerID, 10)].CountryCode != "" {
		t.Fatalf("blocked peer exposes country: %#v", blocked)
	}
}

func testOfficialContactsPagination(t *testing.T, db *gorm.DB, svc *Service, h *server.Hertz, cookie ut.Header, viewerID, officialID, normalID, now int64) {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	originalDB := svc.db
	svc.db = tx
	defer func() { svc.db = originalDB; tx.Rollback() }()
	if err := tx.Exec(`UPDATE agents SET is_official=true WHERE agent_id=?`, officialID).Error; err != nil {
		t.Fatal(err)
	}
	// The official assistant is the older contact. A descending relation-ID
	// cursor alone would miss the newer ordinary contact after page one.
	if err := tx.Exec(`INSERT INTO user_relations (from_uid, to_uid, rel_type, created_at) VALUES (?, ?, 1, ?), (?, ?, 1, ?)`, viewerID, officialID, now-1000, viewerID, normalID, now).Error; err != nil {
		t.Fatal(err)
	}
	// Without official contacts, the existing newest-relation-first order stays.
	if err := tx.Exec(`UPDATE agents SET is_official=false WHERE agent_id=?`, officialID).Error; err != nil {
		t.Fatal(err)
	}
	status, payload, _ := performJSON(t, h, http.MethodGet, "/api/v2/console/relations/friends?limit=2", nil, cookie)
	if status != http.StatusOK {
		t.Fatalf("ordinary friends status=%d payload=%#v", status, payload)
	}
	ordinary := payload["data"].(map[string]interface{})["friends"].([]interface{})
	if len(ordinary) != 2 || ordinary[0].(map[string]interface{})["peer_agent_id"] != strconv.FormatInt(normalID, 10) {
		t.Fatalf("ordinary contacts reordered: %#v", ordinary)
	}
	if err := tx.Exec(`UPDATE agents SET is_official=true WHERE agent_id=?`, officialID).Error; err != nil {
		t.Fatal(err)
	}
	cursor := ""
	for index, wantID := range []int64{officialID, normalID} {
		path := "/api/v2/console/relations/friends?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		status, payload, _ := performJSON(t, h, http.MethodGet, path, nil, cookie)
		if status != http.StatusOK {
			t.Fatalf("friends status=%d payload=%#v", status, payload)
		}
		data := payload["data"].(map[string]interface{})
		rows := data["friends"].([]interface{})
		if len(rows) != 1 || rows[0].(map[string]interface{})["peer_agent_id"] != strconv.FormatInt(wantID, 10) {
			t.Fatalf("page %d friends=%#v, want %d", index, rows, wantID)
		}
		if data["total"] != float64(2) || data["has_more"] != (index == 0) {
			t.Fatalf("page %d metadata=%#v", index, data)
		}
		cursor, _ = data["next_cursor"].(string)
		if (cursor != "") != (index == 0) {
			t.Fatalf("page %d cursor=%q", index, cursor)
		}
	}
}
