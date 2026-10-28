package consolev2

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestAgentAccessFailureReasonsAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, mutation, header, reason string
		status                         int
	}{
		{"missing", "", "", "missing_authorization_header", 401},
		{"malformed", "", "Bearer password=private", "invalid_authorization_header", 401},
		{"not found", "DELETE FROM agent_credential_sessions", "Bearer efv2a_secret", "credential_not_found", 401},
		{"expired", "UPDATE agent_credential_sessions SET expires_at=0", "Bearer efv2a_secret", "access_expired", 401},
		{"revoked", "UPDATE agent_credential_sessions SET revoked_at=1", "Bearer efv2a_secret", "credential_revoked", 401},
		{"principal revoked", "UPDATE agent_principals SET revoked_at=1", "Bearer efv2a_secret", "principal_revoked", 401},
		{"principal inactive", "UPDATE agent_principals SET status='revoked'", "Bearer efv2a_secret", "principal_inactive", 401},
		{"identity inactive", "UPDATE agents SET identity_state='recovered_temporary'", "Bearer efv2a_secret", "identity_inactive", 401},
		{"audience", "UPDATE agent_credential_sessions SET audience='console'", "Bearer efv2a_secret", "audience_invalid", 401},
		{"recovery stale", "UPDATE agent_credential_sessions SET access_refresh_required=TRUE", "Bearer efv2a_secret", "access_refresh_required", 401},
		{"scope", "UPDATE agent_credential_sessions SET scopes='{}'", "Bearer efv2a_secret", "scope_missing", 403},
		{"onboarding", "UPDATE agent_credential_sessions SET scopes='{}';UPDATE agent_onboarding_v2 SET state='in_progress'", "Bearer efv2a_secret", "onboarding_required", 409},
		{"database", "", "Bearer efv2a_secret", "credential_lookup_failed", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
			if err != nil {
				t.Fatal(err)
			}
			conn, _ := db.DB()
			conn.SetMaxOpenConns(1)
			defer conn.Close()
			for _, q := range []string{
				"CREATE TABLE agents(agent_id INTEGER,identity_state TEXT)",
				"CREATE TABLE agent_principals(principal_id INTEGER,agent_id INTEGER,status TEXT,revoked_at INTEGER)",
				"CREATE TABLE agent_credential_sessions(session_id INTEGER,principal_id INTEGER,access_token_hash TEXT,audience TEXT,scopes TEXT,revoked_at INTEGER,expires_at INTEGER,access_refresh_required BOOLEAN)",
				"CREATE TABLE agent_onboarding_v2(agent_id INTEGER,state TEXT)",
				"INSERT INTO agents VALUES(42,'active')",
				"INSERT INTO agent_principals VALUES(7,42,'active',NULL)",
				"INSERT INTO agent_onboarding_v2 VALUES(42,'completed')",
			} {
				if err := db.Exec(q).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Exec("INSERT INTO agent_credential_sessions VALUES(9,7,?,'agent_v2','{feed:read}',NULL,?,FALSE)", hashString("efv2a_secret"), time.Now().Add(time.Hour).UnixMilli()).Error; err != nil {
				t.Fatal(err)
			}
			if tc.mutation != "" {
				if err := db.Exec(tc.mutation).Error; err != nil {
					t.Fatal(err)
				}
			}
			var logs bytes.Buffer
			svc := &Service{db: db.Session(&gorm.Session{Logger: gormlogger.New(log.New(&logs, "", 0), gormlogger.Config{LogLevel: gormlogger.Info})})}
			if tc.name == "database" {
				if err := db.Callback().Row().Before("gorm:row").Register("test:lookup", func(tx *gorm.DB) {
					tx.AddError(&pgconn.PgError{Code: "08006", Message: "password=private SELECT efv2a_secret"})
				}); err != nil {
					t.Fatal(err)
				}
			}
			h := server.New()
			called := false
			traceID, _ := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
			spanID, _ := trace.SpanIDFromHex("0123456789abcdef")
			h.GET("/protected/:id", func(ctx context.Context, c *app.RequestContext) {
				c.Next(trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID})))
			}, svc.agentAuth("feed:read"), func(_ context.Context, c *app.RequestContext) { called = true; c.JSON(200, nil) })
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(prev)
			status, _, _ := performJSON(t, h, "GET", "/protected/private?token=query-secret", nil, ut.Header{Key: "Authorization", Value: tc.header})
			if status != tc.status || called {
				t.Fatalf("status=%d handler=%v", status, called)
			}
			var record map[string]interface{}
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
				t.Fatalf("expected one log: %v %s", err, logs.String())
			}
			if record["traceId"] != traceID.String() {
				t.Fatalf("missing trace %#v", record)
			}
			if record["reason"] != tc.reason || record["stage"] != "agent_access" || record["route"] != "/protected/:id" {
				t.Fatalf("bad log %#v", record)
			}
			if tc.name != "missing" && tc.name != "malformed" && tc.name != "not found" && tc.name != "database" {
				if record["agent_id"] != float64(42) || record["session_id"] != float64(9) {
					t.Fatalf("missing IDs %#v", record)
				}
			}
			if tc.name == "database" && record["sql_state"] != "08006" {
				t.Fatalf("missing sqlstate %#v", record)
			}
			for _, secret := range []string{"efv2a_secret", hashString("efv2a_secret"), "password=", "SELECT", "query-secret", "/protected/private"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("secret in log: %s", secret)
				}
			}
		})
	}
}
