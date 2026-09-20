package main

import (
	"context"
	"os"
	"testing"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/auth"
	shareddb "eigenflux_server/pkg/db"
	"github.com/jackc/pgx/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestAgentV2RPCSessionValidationPostgres(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL session authorization contracts")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		t.Fatal("session authorization tests require a loopback PostgreSQL host")
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
		`CREATE TEMP TABLE agent_credential_sessions (access_token_hash TEXT, principal_id BIGINT, audience TEXT, scopes TEXT[], revoked_at BIGINT, expires_at BIGINT, access_refresh_required BOOLEAN) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_onboarding_v2 (agent_id BIGINT PRIMARY KEY, state TEXT) ON COMMIT DROP`,
	} {
		exec(statement)
	}
	priorDB := shareddb.DB
	shareddb.DB = tx
	t.Cleanup(func() { shareddb.DB = priorDB })
	service := &AuthServiceImpl{}
	token := "efv2a_authorization_contract"

	for _, tc := range []struct {
		name     string
		mutation string
		code     int32
	}{
		{"completed and authorized", "", 0},
		{"baseline credentials", `UPDATE agent_onboarding_v2 SET state='in_progress'; UPDATE agent_principals SET status='limited'; UPDATE agent_credential_sessions SET scopes=ARRAY['feed:read']`, 409},
		{"missing onboarding", `DELETE FROM agent_onboarding_v2`, 409},
		{"missing communication scope", `UPDATE agent_credential_sessions SET scopes=ARRAY['feed:read']`, 403},
		{"limited principal remains restricted", `UPDATE agent_principals SET status='limited'`, 403},
		{"expired credential", `UPDATE agent_credential_sessions SET expires_at=0`, 401},
		{"recovery requires refresh", `UPDATE agent_credential_sessions SET access_refresh_required=TRUE`, 401},
		{"revoked credential", `UPDATE agent_credential_sessions SET revoked_at=1`, 401},
		{"revoked principal", `UPDATE agent_principals SET revoked_at=1`, 401},
		{"inactive principal", `UPDATE agent_principals SET status='revoked'`, 401},
		{"inactive identity", `UPDATE agents SET identity_state='recovered_temporary'`, 401},
		{"wrong audience", `UPDATE agent_credential_sessions SET audience='console'`, 401},
		{"invalid token", `UPDATE agent_credential_sessions SET access_token_hash='another-token'`, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec(`TRUNCATE agents, agent_principals, agent_credential_sessions, agent_onboarding_v2`)
			exec(`INSERT INTO agents VALUES (42, 'active')`)
			exec(`INSERT INTO agent_principals VALUES (7, 42, 'active', NULL)`)
			exec(`INSERT INTO agent_onboarding_v2 VALUES (42, 'completed')`)
			exec(`INSERT INTO agent_credential_sessions VALUES (?,7,'agent_v2',ARRAY['feed:read','communication:read'],NULL,?,FALSE)`, sha256Hex(token), time.Now().Add(time.Hour).UnixMilli())
			if tc.mutation != "" {
				exec(tc.mutation)
			}
			response, err := service.ValidateSession(context.Background(), &auth.ValidateSessionReq{AccessToken: token})
			if err != nil || response == nil || response.BaseResp == nil {
				t.Fatalf("ValidateSession = %#v, %v", response, err)
			}
			if response.BaseResp.Code != tc.code {
				t.Fatalf("code = %d, want %d: %s", response.BaseResp.Code, tc.code, response.BaseResp.Msg)
			}
			if tc.code == 0 && response.AgentId != 42 {
				t.Fatalf("authorized agent = %d, want 42", response.AgentId)
			}
			if tc.code != 0 && response.AgentId != 0 {
				t.Fatalf("rejected session exposed authorized agent = %d", response.AgentId)
			}
		})
	}

	exec(`ALTER TABLE agent_credential_sessions RENAME COLUMN access_token_hash TO unavailable_token_hash`)
	response, err := service.ValidateSession(context.Background(), &auth.ValidateSessionReq{AccessToken: token})
	if err != nil || response == nil || response.BaseResp == nil || response.BaseResp.Code != 503 {
		t.Fatalf("database failure must remain distinct from invalid credentials: %#v, %v", response, err)
	}
}
