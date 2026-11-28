package consolev2

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func validInstallEvent() installLifecycleEvent {
	return installLifecycleEvent{EventID: strings.Repeat("a", 32), EventAt: time.Now().UnixMilli(), EventType: "install_started", Result: "started"}
}

func TestInstallLifecyclePrivacyAndEvidenceValidation(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		mutate func(*installLifecycleEvent)
	}{
		{"server spoof", func(e *installLifecycleEvent) { e.EventType = "server_registration" }},
		{"after-registration anonymous", func(e *installLifecycleEvent) { e.EventType = "credentials_saved" }},
		{"path", func(e *installLifecycleEvent) { e.Component = "/Users/private/secret" }},
		{"raw error", func(e *installLifecycleEvent) { e.Reason = "token=secret" }},
		{"runtime free text", func(e *installLifecycleEvent) { e.RuntimeName = "private-machine" }},
		{"future", func(e *installLifecycleEvent) { e.EventAt = now.Add(2 * time.Minute).UnixMilli() }},
		{"old", func(e *installLifecycleEvent) { e.EventAt = now.Add(-8 * 24 * time.Hour).UnixMilli() }},
		{"extra choice", func(e *installLifecycleEvent) { e.Choice = "manual" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := validInstallEvent()
			tc.mutate(&e)
			if validateInstallLifecycleEvent(e, false, now) == nil {
				t.Fatal("unsafe observation accepted")
			}
		})
	}
	for name, anonymous := range installClientEvents {
		e := validInstallEvent()
		e.EventType = name
		if err := validateInstallLifecycleEvent(e, true, now); err != nil {
			t.Fatalf("authenticated %s: %v", name, err)
		}
		if anonymous {
			if err := validateInstallLifecycleEvent(e, false, now); err != nil {
				t.Fatalf("anonymous %s: %v", name, err)
			}
		}
	}
	for _, runtime := range []string{"deepseek", "deepseek-harness", "dsh"} {
		e := validInstallEvent()
		e.RuntimeName = runtime
		if err := validateInstallLifecycleEvent(e, false, now); err != nil {
			t.Fatalf("supported DeepSeek alias %s: %v", runtime, err)
		}
	}
}

func TestInstallLifecycleRejectsUnknownFieldsBeforeStorage(t *testing.T) {
	svc := &Service{telemetryRates: map[string]telemetryRateState{}, otpPepper: "test"}
	h := server.New()
	h.POST("/anonymous", svc.recordAnonymousInstallLifecycle)
	for _, body := range []string{
		`{"install_session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","agent_id":42,"events":[]}`,
		`{"install_session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","events":[],"proof":"secret"}`,
		strings.Repeat(" ", 33<<10),
	} {
		r := ut.PerformRequest(h.Engine, "POST", "/anonymous", &ut.Body{Body: strings.NewReader(body), Len: len(body)}, ut.Header{Key: "X-Install-Session-Proof", Value: strings.Repeat("b", 64)})
		if r.Code != 400 {
			t.Fatalf("unsafe request status %d", r.Code)
		}
	}
}

func newInstallLifecyclePostgres(t *testing.T) (*gorm.DB, *Service, *server.Hertz) {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required for installation lifecycle PostgreSQL contracts")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if (!strings.HasPrefix(config.Host, "/") && config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") || !strings.HasSuffix(config.Database, "_test") {
		t.Fatal("local PostgreSQL and a database ending in _test are required")
	}
	adminPool := stdlib.OpenDB(*config)
	admin, err := gorm.Open(postgres.New(postgres.Config{Conn: adminPool}), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	schema := "install_lifecycle_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	pool := stdlib.OpenDB(*config)
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); admin.Exec("DROP SCHEMA " + schema + " CASCADE"); adminPool.Close() })
	exec := func(sql string, args ...interface{}) {
		t.Helper()
		if err := gdb.Exec(sql, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, sql := range []string{
		`CREATE TABLE agents(agent_id BIGINT PRIMARY KEY,identity_state TEXT)`,
		`CREATE TABLE agent_principals(principal_id BIGINT PRIMARY KEY,agent_id BIGINT,status TEXT,revoked_at BIGINT)`,
		`CREATE TABLE agent_credential_sessions(session_id BIGINT,access_token_hash TEXT,principal_id BIGINT,audience TEXT,scopes TEXT[],revoked_at BIGINT,expires_at BIGINT,access_refresh_required BOOLEAN)`,
		`CREATE TABLE agent_onboarding_v2(agent_id BIGINT PRIMARY KEY,state TEXT)`,
		`CREATE TABLE telemetry_events_v2(event_id TEXT,agent_id BIGINT,console_session_id TEXT,event_type TEXT,event_at BIGINT,created_at BIGINT)`,
	} {
		exec(sql)
	}
	migration, err := os.ReadFile("../../migrations/000145_install_lifecycle_telemetry.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	exec(up)
	for _, id := range []int64{42, 43} {
		exec(`INSERT INTO agents VALUES (?,'active')`, id)
		exec(`INSERT INTO agent_principals VALUES (?,?,'active',NULL)`, id, id)
		exec(`INSERT INTO agent_onboarding_v2 VALUES (?,'draft')`, id)
		exec(`INSERT INTO agent_credential_sessions VALUES (?,?,?,'agent_v2',ARRAY['onboarding:write'],NULL,?,FALSE)`, id, hashString(fmt.Sprintf("efv2a_install_%d", id)), id, time.Now().Add(time.Hour).UnixMilli())
	}
	svc := &Service{db: gdb, telemetryRates: map[string]telemetryRateState{}, otpPepper: "install-test"}
	h := server.New()
	h.POST("/api/v2/install-telemetry/events:batch", svc.recordAnonymousInstallLifecycle)
	h.POST("/api/v2/agents/me/install-telemetry/events:batch", svc.agentAuthAny("onboarding:write", "settings:write"), svc.recordAuthenticatedInstallLifecycle)
	return gdb, svc, h
}

func TestInstallLifecyclePostgresBindingIdempotencyAndLimits(t *testing.T) {
	db, _, h := newInstallLifecyclePostgres(t)
	session, proof := strings.Repeat("c", 32), strings.Repeat("d", 64)
	e := validInstallEvent()
	body := installLifecycleBatch{InstallSessionID: session, Events: []installLifecycleEvent{e}}
	send := func(actor int64, proofValue string) int {
		path := "/api/v2/install-telemetry/events:batch"
		headers := []ut.Header{{Key: "X-Install-Session-Proof", Value: proofValue}}
		if actor > 0 {
			path = "/api/v2/agents/me/install-telemetry/events:batch"
			headers = append(headers, ut.Header{Key: "Authorization", Value: fmt.Sprintf("Bearer efv2a_install_%d", actor)})
		}
		status, _, _ := performJSON(t, h, "POST", path, body, headers...)
		return status
	}
	for _, actor := range []int64{0, 0, 42} {
		if status := send(actor, proof); status != 202 {
			t.Fatalf("actor %d status %d", actor, status)
		}
	}
	var count int64
	db.Raw(`SELECT count(*) FROM install_lifecycle_events WHERE agent_id IS NULL`).Scan(&count)
	if count != 1 {
		t.Fatal("anonymous evidence mutated or replay duplicated")
	}
	var bound int64
	db.Raw(`SELECT agent_id FROM install_lifecycle_sessions WHERE install_session_id=?`, session).Scan(&bound)
	if bound != 42 {
		t.Fatal("restricted authenticated identity was not bound")
	}
	if status := send(43, proof); status != 409 {
		t.Fatalf("rebind returned %d", status)
	}
	if status := send(42, strings.Repeat("e", 64)); status != 403 {
		t.Fatalf("wrong proof returned %d", status)
	}
	body.Events[0].Result = "failed"
	if status := send(42, proof); status != 409 {
		t.Fatalf("conflicting replay returned %d", status)
	}
	body.Events[0] = e
	if err := db.Exec(`UPDATE install_lifecycle_sessions SET event_count=512 WHERE install_session_id=?`, session).Error; err != nil {
		t.Fatal(err)
	}
	if status := send(42, proof); status != 202 {
		t.Fatalf("quota must permit exact replay: %d", status)
	}
	body.Events[0].EventID = strings.Repeat("f", 32)
	if status := send(42, proof); status != 429 {
		t.Fatalf("quota returned %d", status)
	}
	db.Raw(`SELECT count(*) FROM install_lifecycle_events`).Scan(&count)
	if count != 1 {
		t.Fatal("quota failure did not roll back event")
	}
	// PostgreSQL serializes actual competing identity bindings; one wins and the
	// other must fail. The proof does not decide which Agent identity is trusted.
	concurrentSession := strings.Repeat("1", 32)
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := lockInstallLifecycleSession(tx, concurrentSession, proof, "ip", nil, time.Now().UnixMilli(), true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, actor := range []int64{42, 43} {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			results <- db.Transaction(func(tx *gorm.DB) error {
				_, err := lockInstallLifecycleSession(tx, concurrentSession, proof, "ip", &id, time.Now().UnixMilli(), false)
				return err
			})
		}(actor)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if err == errInstallBinding {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent bind results %d/%d", success, conflict)
	}
	// Database-global quota is shared by new sessions, not a per-process map.
	day := time.Now().UnixMilli() / int64(24*time.Hour/time.Millisecond)
	if err := db.Exec(`UPDATE install_lifecycle_limits SET used=10000 WHERE rate_key='global' AND window_at=?`, day).Error; err != nil {
		t.Fatal(err)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := lockInstallLifecycleSession(tx, strings.Repeat("2", 32), proof, "new-ip", nil, time.Now().UnixMilli(), true)
		return err
	})
	if err != errInstallQuota {
		t.Fatalf("global quota: %v", err)
	}
}

func TestInstallLifecyclePostgresServerFactsAndMissingCorrelation(t *testing.T) {
	db, svc, h := newInstallLifecyclePostgres(t)
	h.POST("/receipt", func(ctx context.Context, c *app.RequestContext) { c.Set("agent_id", int64(42)); c.Next(ctx) }, svc.observeInstallOperation("server_install_receipt"), func(_ context.Context, c *app.RequestContext) { reply(c, 200, map[string]bool{"recorded": false}) })
	h.POST("/confirm", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", int64(42))
		c.Set("console_session_id", "private-session")
		c.Next(ctx)
	}, svc.observeInstallOperation("server_onboarding_step"), func(_ context.Context, c *app.RequestContext) {
		reply(c, 200, map[string]interface{}{"state": "completed", "revision": 7, "secret": "must-not-persist"})
	})
	h.POST("/draft-failed", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", int64(42))
		c.Next(ctx)
	}, svc.observeInstallOperation("server_profile_draft"), func(_ context.Context, c *app.RequestContext) {
		fail(c, 400, "INVALID_DRAFT", "private error content must-not-persist", nil)
	})
	for _, path := range []string{"/receipt", "/confirm"} {
		status, _, _ := performJSON(t, h, "POST", path, map[string]interface{}{"step": 5, "draft": "private draft"})
		if status != 200 {
			t.Fatal(status)
		}
	}
	if status, _, _ := performJSON(t, h, "POST", "/draft-failed", map[string]interface{}{}); status != 400 {
		t.Fatalf("business error changed: %d", status)
	}
	var rows []struct {
		EventType, Result, Evidence, Properties string
		InstallSessionID                        *string
		AgentID                                 int64
	}
	if err := db.Raw(`SELECT event_type,result,evidence,properties::text,install_session_id,agent_id FROM install_lifecycle_events ORDER BY event_type`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("server facts: %+v", rows)
	}
	for _, row := range rows {
		if row.InstallSessionID != nil || row.AgentID != 42 || row.Evidence != "server_observed" {
			t.Fatalf("invented correlation: %+v", row)
		}
		if strings.Contains(row.Properties, "private") || strings.Contains(row.Properties, "must-not-persist") {
			t.Fatal("content retained")
		}
		if row.EventType == "server_install_receipt" && row.Result != "skipped" {
			t.Fatal("unsupported receipt reported successful")
		}
		if row.EventType == "server_onboarding_step" {
			var props map[string]interface{}
			json.Unmarshal([]byte(row.Properties), &props)
			if props["state"] != "completed" || props["step"] != float64(5) || props["journey_id"] != hashString("private-session") {
				t.Fatalf("completion evidence: %s", row.Properties)
			}
		}
		if row.EventType == "server_profile_draft" && row.Result != "failed" {
			t.Fatal("failed draft reported successful")
		}
	}
	for _, view := range []string{"grafana_install_lifecycle_sessions", "grafana_install_lifecycle_events"} {
		result, err := db.Raw("SELECT * FROM " + view + " LIMIT 0").Rows()
		if err != nil {
			t.Fatal(err)
		}
		columns, err := result.Columns()
		result.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, column := range columns {
			if column == "proof_hash" || column == "payload_hash" {
				t.Fatal("dashboard exposes a verifier")
			}
		}
	}
	if err := db.Exec(`INSERT INTO telemetry_events_v2 VALUES ('render',42,'private-session','dashboard_first_render',1,2)`).Error; err != nil {
		t.Fatal(err)
	}
	var journey string
	if err := db.Raw(`SELECT journey_id FROM grafana_install_browser_events`).Scan(&journey).Error; err != nil {
		t.Fatal(err)
	}
	if journey != hashString("private-session") {
		t.Fatal("browser and server journeys do not match")
	}
}

func TestInstallLifecyclePostgresServerQuotaPreservesBusinessResponse(t *testing.T) {
	db, svc, h := newInstallLifecyclePostgres(t)
	h.POST("/invalid-handoff", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", int64(42))
		c.Set("principal_id", int64(42))
		c.Next(ctx)
	}, svc.observeInstallOperation("server_handoff"), svc.createHandoff)
	day := time.Now().UnixMilli() / int64(24*time.Hour/time.Millisecond)
	if err := db.Exec(`INSERT INTO install_lifecycle_limits VALUES ('server-agent:42',?,511)`, day).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		status, _, _ := performJSON(t, h, "POST", "/invalid-handoff", map[string]interface{}{})
		if status != 400 {
			t.Fatalf("telemetry quota changed invalid handoff response: %d", status)
		}
	}
	var count, used int64
	if err := db.Raw(`SELECT count(*) FROM install_lifecycle_events WHERE agent_id=42`).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT used FROM install_lifecycle_limits WHERE rate_key='server-agent:42' AND window_at=?`, day).Scan(&used).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 || used != 512 {
		t.Fatalf("unbounded server observations count=%d used=%d", count, used)
	}
}
