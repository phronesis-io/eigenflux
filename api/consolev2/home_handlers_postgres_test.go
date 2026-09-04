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
		ConsoleV2BootstrapSecret: "home-contract-secret",
		ConsoleV2OTPPepper:       "home-contract-pepper",
		ConsoleV2PublicURL:       "https://console.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.redisClient = redisClient
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
	if err := db.Exec(`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES
		(?, ?, 'new Agent first voice', ?), (?, ?, 'older demand', ?),
		(?, ?, 'newer demand', ?), (?, ?, 'newest general publish', ?)`,
		firstVoiceItemID, agentIDValue, now-4000,
		oldDemandItemID, demandAgentID, now-3000,
		newDemandItemID, demandAgentID, now-2000,
		newPublishItemID, publishAgentID, now-1000).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO processed_items
		(item_id, status, summary, broadcast_type, quality_score, lang, updated_at,
		 homepage_eligible, homepage_evaluation_version, homepage_real_world_relevant)
		VALUES (?, 3, 'new Agent first voice', 'info', 0.70, 'en', ?, TRUE, ?, FALSE),
		       (?, 3, 'older demand', 'demand', 0.95, 'en', ?, TRUE, ?, FALSE),
		       (?, 3, 'newer demand', 'demand', 0.80, 'en', ?, TRUE, ?, FALSE),
		       (?, 3, 'newest general publish', 'info', 0.99, 'en', ?, TRUE, ?, FALSE)`,
		firstVoiceItemID, now, homepageEvaluationVersion,
		oldDemandItemID, now, homepageEvaluationVersion,
		newDemandItemID, now, homepageEvaluationVersion,
		newPublishItemID, now, homepageEvaluationVersion).Error; err != nil {
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
		_ = db.Exec(`DELETE FROM processed_items WHERE item_id IN (?, ?, ?, ?)`, firstVoiceItemID, oldDemandItemID, newDemandItemID, newPublishItemID).Error
		_ = db.Exec(`DELETE FROM raw_items WHERE item_id IN (?, ?, ?, ?)`, firstVoiceItemID, oldDemandItemID, newDemandItemID, newPublishItemID).Error
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
