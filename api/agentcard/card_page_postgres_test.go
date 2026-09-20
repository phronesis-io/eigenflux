package agentcardapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/jackc/pgx/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/db"
)

func TestMyCardPagePostgres(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for Agent Card page PostgreSQL contracts")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		t.Fatal("Agent Card page tests require a loopback PostgreSQL host")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	for _, tc := range []struct {
		name       string
		agentName  string
		mutation   string
		wantStatus int
	}{
		{"empty name", "", "", http.StatusOK},
		{"named agent", "Atlas", "", http.StatusOK},
		{"missing agent with retained projection", "", "DELETE FROM agents", http.StatusInternalServerError},
		{"snapshot query error", "", "ALTER TABLE agents RENAME COLUMN agent_name TO unavailable_name", http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := gdb.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			t.Cleanup(func() { tx.Rollback() })
			previousDB := db.DB
			db.DB = tx
			t.Cleanup(func() { db.DB = previousDB })
			exec := func(statement string, args ...interface{}) {
				t.Helper()
				if err := tx.Exec(statement, args...).Error; err != nil {
					t.Fatal(err)
				}
			}
			for _, statement := range []string{
				`CREATE TEMP TABLE agents (agent_id BIGINT PRIMARY KEY, agent_name TEXT, short_id TEXT, bio TEXT, is_official BOOLEAN) ON COMMIT DROP`,
				`CREATE TEMP TABLE agent_email_bindings (agent_id BIGINT, status TEXT, verification_state TEXT) ON COMMIT DROP`,
				`CREATE TEMP TABLE agent_profiles (agent_id BIGINT, profile_version BIGINT, profile_data JSONB) ON COMMIT DROP`,
				`CREATE TEMP TABLE agent_network_memberships (agent_id BIGINT, member_no BIGINT) ON COMMIT DROP`,
				`CREATE TEMP TABLE agent_network_goals (agent_id BIGINT, goal_text TEXT, status TEXT) ON COMMIT DROP`,
				`CREATE TEMP TABLE agent_intent_actions (agent_id BIGINT, intent_id BIGINT, watch_for TEXT, priority INTEGER, status TEXT) ON COMMIT DROP`,
				`CREATE TEMP TABLE agent_cards (agent_id BIGINT PRIMARY KEY, public_card JSONB, private_card JSONB, schema_version INTEGER, source_version BIGINT, rebuild_fence BIGINT, card_version BIGINT, public_card_version BIGINT, generated_at BIGINT, public_card_generated_at BIGINT) ON COMMIT DROP`,
			} {
				exec(statement)
			}
			exec(`INSERT INTO agents VALUES (42, ?, 'AbcDe', 'Research assistant', FALSE)`, tc.agentName)
			exec(`INSERT INTO agent_cards VALUES (42, '{"agent_id":"42"}', '{}', ?, 1, 0, 1, 1, 100, 100)`, agentcard.SchemaVersion)
			if tc.mutation != "" {
				exec(tc.mutation)
			}
			h := server.New()
			const path = "/api/v2/console/bff/agents/me/card/page"
			h.GET(path, func(_ context.Context, c *app.RequestContext) { c.Set("agent_id", int64(42)) }, GetMyCardPage)
			response := ut.PerformRequest(h.Engine, http.MethodGet, path, nil).Result()
			if response.StatusCode() != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.StatusCode(), tc.wantStatus, response.Body())
			}
			var payload struct {
				Code int `json:"code"`
				Data struct {
					CurrentValues map[string]interface{} `json:"current_values"`
					Card          struct {
						Public map[string]interface{} `json:"public"`
					} `json:"card"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body(), &payload); err != nil {
				t.Fatal(err)
			}
			if tc.wantStatus != http.StatusOK {
				if payload.Code != 500 {
					t.Fatalf("error code = %d, want 500", payload.Code)
				}
				return
			}
			if payload.Code != 0 || payload.Data.CurrentValues["agent_name"] != tc.agentName || payload.Data.CurrentValues["agent_description"] != "Research assistant" {
				t.Fatalf("current identity was not preserved: %s", response.Body())
			}
			wantDisplayName := tc.agentName
			if wantDisplayName == "" {
				wantDisplayName = "Agent #AbcDe"
			}
			if payload.Data.Card.Public["display_name"] != wantDisplayName {
				t.Fatalf("display_name = %v, want %q", payload.Data.Card.Public["display_name"], wantDisplayName)
			}
		})
	}
}
