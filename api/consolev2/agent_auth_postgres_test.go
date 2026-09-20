package consolev2

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/jackc/pgx/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestAgentAuthorizationPostgres(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL Agent authorization contracts")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		t.Fatal("Agent authorization tests require a loopback PostgreSQL host")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	tx := gdb.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	exec := func(sql string, args ...interface{}) {
		t.Helper()
		if err := tx.Exec(sql, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`CREATE TEMP TABLE agents (agent_id BIGINT PRIMARY KEY, identity_state TEXT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_principals (principal_id BIGINT PRIMARY KEY, agent_id BIGINT, status TEXT, revoked_at BIGINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_credential_sessions (session_id BIGINT, access_token_hash TEXT, principal_id BIGINT, audience TEXT, scopes TEXT[], revoked_at BIGINT, expires_at BIGINT, access_refresh_required BOOLEAN) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_onboarding_v2 (agent_id BIGINT PRIMARY KEY, state TEXT) ON COMMIT DROP`,
	} {
		exec(statement)
	}
	svc := &Service{db: tx}
	h := server.New()
	allowed := func(_ context.Context, c *app.RequestContext) { c.JSON(200, map[string]bool{"allowed": true}) }
	h.GET("/auth/feed", svc.agentAuth("feed:read"), allowed)
	h.GET("/auth/messages", svc.agentAuth("communication:read"), allowed)
	h.GET("/auth/any", svc.agentAuthAny("communication:read", "feed:read"), allowed)
	token := "efv2a_authorization_contract"
	for _, tc := range []struct {
		name     string
		path     string
		mutation string
		status   int
		code     string
	}{
		{"baseline Feed allowed", "/auth/feed", "", 200, ""},
		{"one matching scope allowed", "/auth/any", "", 200, ""},
		{"baseline private messages restricted", "/auth/messages", "", 409, "ONBOARDING_REQUIRED"},
		{"missing onboarding keeps Feed", "/auth/feed", `DELETE FROM agent_onboarding_v2`, 200, ""},
		{"missing onboarding restricts messages", "/auth/messages", `DELETE FROM agent_onboarding_v2`, 409, "ONBOARDING_REQUIRED"},
		{"completed session missing scope", "/auth/messages", `UPDATE agent_onboarding_v2 SET state='completed'; UPDATE agent_principals SET status='active'`, 403, "AGENT_SCOPE_REQUIRED"},
		{"completion upgrades same token", "/auth/messages", `UPDATE agent_onboarding_v2 SET state='completed'; UPDATE agent_principals SET status='active'; UPDATE agent_credential_sessions SET scopes=ARRAY['feed:read','communication:read']`, 200, ""},
		{"expired credential", "/auth/feed", `UPDATE agent_credential_sessions SET expires_at=0`, 401, "AGENT_AUTH_INVALID"},
		{"recovery requires refresh", "/auth/feed", `UPDATE agent_credential_sessions SET access_refresh_required=TRUE`, 401, "AGENT_AUTH_INVALID"},
		{"revoked credential", "/auth/feed", `UPDATE agent_credential_sessions SET revoked_at=1`, 401, "AGENT_AUTH_INVALID"},
		{"revoked principal", "/auth/feed", `UPDATE agent_principals SET revoked_at=1`, 401, "AGENT_AUTH_INVALID"},
		{"inactive identity", "/auth/feed", `UPDATE agents SET identity_state='recovered_temporary'`, 401, "AGENT_AUTH_INVALID"},
		{"wrong audience", "/auth/feed", `UPDATE agent_credential_sessions SET audience='console'`, 401, "AGENT_AUTH_INVALID"},
		{"invalid token", "/auth/feed", `UPDATE agent_credential_sessions SET access_token_hash='another-token'`, 401, "AGENT_AUTH_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec(`TRUNCATE agents, agent_principals, agent_credential_sessions, agent_onboarding_v2`)
			exec(`INSERT INTO agents VALUES (42, 'active')`)
			exec(`INSERT INTO agent_principals VALUES (7, 42, 'limited', NULL)`)
			exec(`INSERT INTO agent_onboarding_v2 VALUES (42, 'in_progress')`)
			exec(`INSERT INTO agent_credential_sessions VALUES (9,?,7,'agent_v2',ARRAY['feed:read'],NULL,?,FALSE)`, hashString(token), time.Now().Add(time.Hour).UnixMilli())
			if tc.mutation != "" {
				exec(tc.mutation)
			}
			status, payload, _ := performJSON(t, h, "GET", tc.path, nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
			if status != tc.status {
				t.Fatalf("status = %d, want %d: %#v", status, tc.status, payload)
			}
			if tc.code != "" && responseErrorCode(t, payload) != tc.code {
				t.Fatalf("unexpected error: %#v", payload)
			}
		})
	}
	status, payload, _ := performJSON(t, h, "GET", "/auth/feed", nil)
	if status != 401 || responseErrorCode(t, payload) != "AGENT_AUTH_REQUIRED" {
		t.Fatalf("missing bearer = %d, %#v", status, payload)
	}
	exec(`ALTER TABLE agent_credential_sessions RENAME COLUMN access_token_hash TO unavailable_token_hash`)
	status, payload, _ = performJSON(t, h, "GET", "/auth/feed", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
	if status != 503 || responseErrorCode(t, payload) != "AGENT_AUTH_UNAVAILABLE" {
		t.Fatalf("database failure must remain distinct from invalid credentials: %d, %#v", status, payload)
	}
}
