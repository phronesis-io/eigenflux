package consolev2

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func refreshTestServer(svc *Service, ctx context.Context) *server.Hertz {
	h := server.New()
	wrap := func(handler app.HandlerFunc) app.HandlerFunc {
		return func(_ context.Context, c *app.RequestContext) { handler(ctx, c) }
	}
	h.POST("/api/v2/agent-sessions/refresh-challenges", wrap(svc.createRefreshChallenge))
	h.POST("/api/v2/agent-sessions/refresh", wrap(svc.refreshAgentSession))
	return h
}

func signedRefreshRequest(t *testing.T, privateKey ed25519.PrivateKey, token, rotation, nonce string) refreshAgentSessionRequest {
	t.Helper()
	req := refreshAgentSessionRequest{
		RefreshToken: token, RotationRequestID: rotation, Nonce: nonce,
		PublicKey: base64.RawURLEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)),
		IssuedAt:  time.Now().UnixMilli(),
	}
	transcript, err := refreshTranscript(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, transcript))
	return req
}

func TestRefreshDatabaseFailureClassificationAndRedaction(t *testing.T) {
	for _, stage := range []string{"challenge", "refresh"} {
		t.Run(stage, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.New(log.New(&logs, "", 0), gormlogger.Config{LogLevel: gormlogger.Info})})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, _ := gdb.DB()
			t.Cleanup(func() { sqlDB.Close() })
			sentinel := &pgconn.PgError{Code: "08006", Message: "password=do-not-log refresh-secret nonce-secret SELECT secret"}
			if err := gdb.Callback().Row().Before("gorm:row").Register("test:refresh_database_error", func(query *gorm.DB) {
				if query.Statement.Context == nil || query.Statement.Context.Value(refreshContextKey{}) != "present" {
					t.Error("database operation lost request context")
				}
				query.AddError(sentinel)
			}); err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(context.Background(), refreshContextKey{}, "present")
			traceID, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
			spanID, _ := trace.SpanIDFromHex("0102030405060708")
			ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID}))
			h := refreshTestServer(&Service{db: gdb}, ctx)
			var body interface{} = refreshChallengeRequest{RefreshToken: "efv2r_refresh-secret", RotationRequestID: "rotation-request-0001"}
			path := "/api/v2/agent-sessions/refresh-challenges"
			if stage == "refresh" {
				_, privateKey, _ := ed25519.GenerateKey(rand.Reader)
				body = signedRefreshRequest(t, privateKey, "efv2r_refresh-secret", "rotation-request-0001", "nonce-secret")
				path = "/api/v2/agent-sessions/refresh"
			}
			status, payload, _ := performJSON(t, h, http.MethodPost, path, body)
			if status != 503 || responseErrorCode(t, payload) != "AGENT_AUTH_UNAVAILABLE" {
				t.Fatalf("database error = %d %#v", status, payload)
			}
			var record map[string]interface{}
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
				t.Fatalf("expected exactly one JSON log: %v %s", err, logs.String())
			}
			if record["stage"] != stage || record["reason"] != "credential_lookup_failed" || record["sql_state"] != "08006" || record["traceId"] != traceID.String() {
				t.Fatalf("missing failure context: %#v", record)
			}
			for _, secret := range []string{"password=", "refresh-secret", "nonce-secret", "SELECT", hashString("efv2r_refresh-secret")} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("sensitive database error leaked: %s", secret)
				}
			}
		})
	}
}

type refreshContextKey struct{}

type refreshPostgresFixture struct {
	db         *gorm.DB
	h          *server.Hertz
	privateKey ed25519.PrivateKey
	token      string
}

func newRefreshPostgresFixture(t *testing.T) *refreshPostgresFixture {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL refresh contracts")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		t.Fatal("refresh tests require a loopback PostgreSQL host")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	// Temporary tables stay on this one dedicated connection; no application
	// tables, migrations, or production credentials are used by this fixture.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	for _, statement := range []string{
		`CREATE TEMP TABLE agents (agent_id BIGINT PRIMARY KEY, identity_state TEXT)`,
		`CREATE TEMP TABLE agent_principals (principal_id BIGINT PRIMARY KEY, agent_id BIGINT, key_fingerprint TEXT, status TEXT, revoked_at BIGINT)`,
		`CREATE TEMP TABLE agent_onboarding_v2 (agent_id BIGINT PRIMARY KEY, state TEXT)`,
		`CREATE TEMP TABLE agent_credential_sessions (session_id BIGSERIAL PRIMARY KEY, principal_id BIGINT, family_id TEXT, access_token_hash TEXT, refresh_token_hash TEXT, audience TEXT, scopes TEXT[], rotation_counter BIGINT DEFAULT 0, issued_at BIGINT, expires_at BIGINT, absolute_expires_at BIGINT, last_seen_at BIGINT, revoked_at BIGINT, replaced_by_session_id BIGINT, rotation_request_id TEXT, rotation_request_hash TEXT, access_refresh_required BOOLEAN DEFAULT FALSE)`,
		`CREATE TEMP TABLE agent_signature_nonces (nonce_hash TEXT PRIMARY KEY, key_fingerprint TEXT, domain TEXT, expires_at BIGINT, created_at BIGINT, consumed_at BIGINT)`,
		`INSERT INTO agents VALUES (42, 'active')`,
		`INSERT INTO agent_onboarding_v2 VALUES (42, 'completed')`,
	} {
		if err := gdb.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_principals VALUES (7, 42, ?, 'active', NULL)`, fingerprint(publicKey)).Error; err != nil {
		t.Fatal(err)
	}
	token := "efv2r_postgres-refresh-contract"
	now := time.Now().UnixMilli()
	if err := gdb.Exec(`INSERT INTO agent_credential_sessions (principal_id,family_id,access_token_hash,refresh_token_hash,audience,scopes,issued_at,expires_at,absolute_expires_at,last_seen_at) VALUES (7,'test-family',?,?,'agent_v2',ARRAY['feed:read'],?,?,?,?)`, hashString("efv2a_original"), hashString(token), now, now+60000, now+60000, now).Error; err != nil {
		t.Fatal(err)
	}
	return &refreshPostgresFixture{db: gdb, h: refreshTestServer(&Service{db: gdb, otpPepper: "test-refresh-pepper"}, context.Background()), privateKey: privateKey, token: token}
}

func (f *refreshPostgresFixture) exec(t *testing.T, sql string, args ...interface{}) {
	t.Helper()
	if err := f.db.Exec(sql, args...).Error; err != nil {
		t.Fatal(err)
	}
}

func (f *refreshPostgresFixture) request(t *testing.T, token, rotation string) refreshAgentSessionRequest {
	t.Helper()
	status, payload, _ := performJSON(t, f.h, http.MethodPost, "/api/v2/agent-sessions/refresh-challenges", refreshChallengeRequest{RefreshToken: token, RotationRequestID: rotation})
	if status != 201 {
		t.Fatalf("challenge = %d %#v", status, payload)
	}
	return signedRefreshRequest(t, f.privateKey, token, rotation, responseData(t, payload)["nonce"].(string))
}

func (f *refreshPostgresFixture) rotate(t *testing.T, token, rotation string) map[string]interface{} {
	t.Helper()
	req := f.request(t, token, rotation)
	status, payload, _ := performJSON(t, f.h, http.MethodPost, "/api/v2/agent-sessions/refresh", req)
	if status != 200 {
		t.Fatalf("refresh = %d %#v", status, payload)
	}
	return responseData(t, payload)
}

func TestAgentRefreshPostgresSlidingAndReplay(t *testing.T) {
	f := newRefreshPostgresFixture(t)
	first := f.rotate(t, f.token, "rotation-request-first")
	var successor struct{ IssuedAt, ExpiresAt, AbsoluteExpiresAt int64 }
	if err := f.db.Raw(`SELECT issued_at, expires_at, absolute_expires_at FROM agent_credential_sessions WHERE session_id=?`, first["session_id"]).Scan(&successor).Error; err != nil {
		t.Fatal(err)
	}
	if successor.ExpiresAt-successor.IssuedAt != int64(accessTTL/time.Millisecond) || successor.AbsoluteExpiresAt-successor.IssuedAt != int64(refreshTTL/time.Millisecond) {
		t.Fatalf("rotation did not grant full sliding lifetimes: %+v", successor)
	}
	// A response lost immediately before the old deadline remains replayable.
	f.exec(t, `UPDATE agent_credential_sessions SET absolute_expires_at=0 WHERE session_id=1`)
	replay := f.rotate(t, f.token, "rotation-request-first")
	for _, key := range []string{"session_id", "access_token", "refresh_token", "expires_at"} {
		if replay[key] != first[key] {
			t.Fatalf("replay changed %s: %#v %#v", key, first, replay)
		}
	}
	var deadline int64
	if err := f.db.Raw(`SELECT absolute_expires_at FROM agent_credential_sessions WHERE session_id=?`, first["session_id"]).Scan(&deadline).Error; err != nil {
		t.Fatal(err)
	}
	if deadline != successor.AbsoluteExpiresAt {
		t.Fatal("idempotent replay extended refresh lifetime")
	}
	// A later genuine rotation renews the 30-day idle window again.
	f.exec(t, `UPDATE agent_credential_sessions SET absolute_expires_at=? WHERE session_id=?`, time.Now().Add(time.Minute).UnixMilli(), first["session_id"])
	second := f.rotate(t, first["refresh_token"].(string), "rotation-request-second")
	var next struct {
		IssuedAt, ExpiresAt, AbsoluteExpiresAt int64
		RotationCounter                        int64
	}
	if err := f.db.Raw(`SELECT issued_at, expires_at, absolute_expires_at, rotation_counter FROM agent_credential_sessions WHERE session_id=?`, second["session_id"]).Scan(&next).Error; err != nil {
		t.Fatal(err)
	}
	if next.AbsoluteExpiresAt-next.IssuedAt != int64(refreshTTL/time.Millisecond) || next.ExpiresAt-next.IssuedAt != int64(accessTTL/time.Millisecond) || next.RotationCounter != 2 {
		t.Fatalf("second rotation did not renew window: %+v", next)
	}
	var count int64
	if err := f.db.Raw(`SELECT count(*) FROM agent_credential_sessions`).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("idempotency created an extra session: %d", count)
	}
	// A different request replay still revokes the entire family.
	status, payload, _ := performJSON(t, f.h, http.MethodPost, "/api/v2/agent-sessions/refresh-challenges", refreshChallengeRequest{RefreshToken: f.token, RotationRequestID: "rotation-request-attacker"})
	if status != 401 || responseErrorCode(t, payload) != "REFRESH_REUSE_DETECTED" {
		t.Fatalf("reuse = %d %#v", status, payload)
	}
	if err := f.db.Raw(`SELECT count(*) FROM agent_credential_sessions WHERE revoked_at IS NULL`).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("reuse left %d active sessions", count)
	}
}

func TestAgentRefreshPostgresExpiryClassification(t *testing.T) {
	for _, tc := range []struct{ name, mutation, challengeCode, refreshCode string }{
		{"expired", "UPDATE agent_credential_sessions SET absolute_expires_at=0", "REFRESH_EXPIRED", "REFRESH_EXPIRED"},
		{"revoked expired", "UPDATE agent_credential_sessions SET absolute_expires_at=0,revoked_at=1", "REFRESH_INVALID", "REFRESH_REUSE_DETECTED"},
		{"principal revoked", "UPDATE agent_credential_sessions SET absolute_expires_at=0; UPDATE agent_principals SET revoked_at=1", "REFRESH_INVALID", "REFRESH_INVALID"},
		{"principal inactive", "UPDATE agent_credential_sessions SET absolute_expires_at=0; UPDATE agent_principals SET status='revoked'", "REFRESH_INVALID", "REFRESH_INVALID"},
		{"identity inactive", "UPDATE agent_credential_sessions SET absolute_expires_at=0; UPDATE agents SET identity_state='deleted'", "REFRESH_INVALID", "REFRESH_INVALID"},
		{"unknown token", "DELETE FROM agent_credential_sessions", "REFRESH_INVALID", "REFRESH_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRefreshPostgresFixture(t)
			f.exec(t, tc.mutation)
			status, payload, _ := performJSON(t, f.h, http.MethodPost, "/api/v2/agent-sessions/refresh-challenges", refreshChallengeRequest{RefreshToken: f.token, RotationRequestID: "rotation-request-expired"})
			if status != 401 || responseErrorCode(t, payload) != tc.challengeCode {
				t.Fatalf("challenge=%d %#v", status, payload)
			}
			req := signedRefreshRequest(t, f.privateKey, f.token, "rotation-request-expired", "unused-nonce")
			status, payload, _ = performJSON(t, f.h, http.MethodPost, "/api/v2/agent-sessions/refresh", req)
			if status != 401 || responseErrorCode(t, payload) != tc.refreshCode {
				t.Fatalf("refresh=%d %#v", status, payload)
			}
		})
	}
}

func TestAgentRefreshPostgresDatabaseFailures(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		stage        string
		replay       bool
	}{
		{"challenge nonce insert", "INSERT INTO agent_signature_nonces", "challenge", false},
		{"nonce consume", "UPDATE agent_signature_nonces", "refresh", false},
		{"credential insert", "INSERT INTO agent_credential_sessions", "refresh", false},
		{"credential retire", "SET revoked_at =", "refresh", false},
		{"replay nonce consume", "UPDATE agent_signature_nonces", "refresh", true},
		{"replay scope update", "SET scopes =", "refresh", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRefreshPostgresFixture(t)
			if tc.replay {
				f.rotate(t, f.token, "rotation-request-failure")
			}
			var req refreshAgentSessionRequest
			if tc.stage == "refresh" {
				req = f.request(t, f.token, "rotation-request-failure")
			}
			failQuery := func(query *gorm.DB) {
				if strings.Contains(query.Statement.SQL.String(), tc.target) {
					query.AddError(errors.New("injected temporary database failure"))
				}
			}
			if err := f.db.Callback().Raw().Before("gorm:raw").Register("test:refresh_error", failQuery); err != nil {
				t.Fatal(err)
			}
			if err := f.db.Callback().Row().Before("gorm:row").Register("test:refresh_error", failQuery); err != nil {
				t.Fatal(err)
			}
			var body interface{} = req
			path := "/api/v2/agent-sessions/refresh"
			if tc.stage == "challenge" {
				body = refreshChallengeRequest{RefreshToken: f.token, RotationRequestID: "rotation-request-failure"}
				path = "/api/v2/agent-sessions/refresh-challenges"
			}
			status, payload, _ := performJSON(t, f.h, http.MethodPost, path, body)
			if status != 503 || responseErrorCode(t, payload) != "AGENT_AUTH_UNAVAILABLE" {
				t.Fatalf("database failure=%d %#v", status, payload)
			}
			var active int64
			if err := f.db.Raw(`SELECT count(*) FROM agent_credential_sessions WHERE revoked_at IS NULL`).Scan(&active).Error; err != nil {
				t.Fatal(err)
			}
			if active != 1 {
				t.Fatalf("failed rotation changed active sessions: %d", active)
			}
			if tc.stage == "refresh" {
				var unused int64
				if err := f.db.Raw(`SELECT count(*) FROM agent_signature_nonces WHERE nonce_hash=? AND consumed_at IS NULL`, hashString(req.Nonce)).Scan(&unused).Error; err != nil {
					t.Fatal(err)
				}
				if unused != 1 {
					t.Fatal("failed transaction consumed its proof nonce")
				}
			}
		})
	}
}

func TestAgentRefreshPostgresNonceInvalidAndKeyMismatch(t *testing.T) {
	f := newRefreshPostgresFixture(t)
	req := f.request(t, f.token, "rotation-request-invalid")
	f.exec(t, `UPDATE agent_signature_nonces SET consumed_at=1`)
	status, payload, _ := performJSON(t, f.h, http.MethodPost, "/api/v2/agent-sessions/refresh", req)
	if status != 401 || responseErrorCode(t, payload) != "REFRESH_INVALID" {
		t.Fatalf("consumed nonce=%d %#v", status, payload)
	}
	f.exec(t, `UPDATE agent_credential_sessions SET absolute_expires_at=0`)
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	req = signedRefreshRequest(t, otherKey, f.token, "rotation-request-invalid", "unused-nonce")
	status, payload, _ = performJSON(t, f.h, http.MethodPost, "/api/v2/agent-sessions/refresh", req)
	if status != 401 || responseErrorCode(t, payload) != "REFRESH_INVALID" {
		t.Fatalf("wrong key must not trigger recovery=%d %#v", status, payload)
	}
}

func TestAgentRefreshPostgresRevocationFailure(t *testing.T) {
	for _, stage := range []string{"challenge", "refresh", "commit"} {
		t.Run(stage, func(t *testing.T) {
			f := newRefreshPostgresFixture(t)
			if stage == "challenge" {
				f.rotate(t, f.token, "rotation-request-first")
			} else {
				f.exec(t, `UPDATE agent_credential_sessions SET revoked_at=1`)
			}
			if stage == "commit" {
				// The revocation SQL succeeds, but its deferred constraint fails at
				// COMMIT. The handler must report infrastructure failure, not reuse.
				f.exec(t, `UPDATE agent_credential_sessions SET revoked_at=NULL`)
				f.exec(t, `CREATE TEMP TABLE revocation_clocks (value BIGINT PRIMARY KEY)`)
				f.exec(t, `ALTER TABLE agent_credential_sessions ADD CONSTRAINT test_deferred_revoke FOREIGN KEY(revoked_at) REFERENCES revocation_clocks(value) DEFERRABLE INITIALLY DEFERRED`)
				f.exec(t, `INSERT INTO revocation_clocks VALUES (1)`)
				f.exec(t, `UPDATE agent_credential_sessions SET revoked_at=1`)
				// Force a mismatching successor, causing family revocation to
				// write a new timestamp absent from revocation_clocks.
				f.exec(t, `UPDATE agent_credential_sessions SET replaced_by_session_id=999`)
				f.exec(t, `INSERT INTO agent_credential_sessions (principal_id,family_id) VALUES (7,'test-family')`)
			} else {
				if err := f.db.Callback().Raw().Before("gorm:raw").Register("test:revoke_failure", func(query *gorm.DB) {
					if strings.Contains(query.Statement.SQL.String(), "COALESCE(revoked_at") {
						query.AddError(errors.New("revocation database failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			var body interface{} = signedRefreshRequest(t, f.privateKey, f.token, "rotation-request-reuse", "unused-nonce")
			path := "/api/v2/agent-sessions/refresh"
			if stage == "challenge" {
				body = refreshChallengeRequest{RefreshToken: f.token, RotationRequestID: "rotation-request-reuse"}
				path = "/api/v2/agent-sessions/refresh-challenges"
			}
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			status, payload, _ := performJSON(t, f.h, http.MethodPost, path, body)
			if status != 503 || responseErrorCode(t, payload) != "AGENT_AUTH_UNAVAILABLE" {
				t.Fatalf("revocation failure=%d %#v", status, payload)
			}
			var record map[string]interface{}
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
				t.Fatalf("expected sanitized revocation failure: %v %s", err, logs.String())
			}
			wantReason := "family_revocation_failed"
			if stage == "commit" {
				wantReason = "transaction_commit_failed"
			}
			if record["reason"] != wantReason {
				t.Fatalf("incorrect revocation failure stage: %#v", record)
			}
			if stage == "commit" {
				var active int64
				if err := f.db.Raw(`SELECT count(*) FROM agent_credential_sessions WHERE revoked_at IS NULL`).Scan(&active).Error; err != nil {
					t.Fatal(err)
				}
				if active != 1 {
					t.Fatal("failed commit persisted family revocation")
				}
			}
		})
	}
}

func TestAgentRefreshPostgresScanFailure(t *testing.T) {
	f := newRefreshPostgresFixture(t)
	f.exec(t, `ALTER TABLE agent_credential_sessions ALTER COLUMN absolute_expires_at TYPE TEXT USING absolute_expires_at::text`)
	f.exec(t, `UPDATE agent_credential_sessions SET absolute_expires_at='invalid-timestamp'`)
	status, payload, _ := performJSON(t, f.h, http.MethodPost, "/api/v2/agent-sessions/refresh-challenges", refreshChallengeRequest{RefreshToken: f.token, RotationRequestID: "rotation-request-scan-error"})
	if status != 503 || responseErrorCode(t, payload) != "AGENT_AUTH_UNAVAILABLE" {
		t.Fatalf("challenge Scan failure=%d %#v", status, payload)
	}
	req := signedRefreshRequest(t, f.privateKey, f.token, "rotation-request-scan-error", "unused-nonce")
	status, payload, _ = performJSON(t, f.h, http.MethodPost, "/api/v2/agent-sessions/refresh", req)
	if status != 503 || responseErrorCode(t, payload) != "AGENT_AUTH_UNAVAILABLE" {
		t.Fatalf("refresh Scan failure=%d %#v", status, payload)
	}
}

func TestAgentRefreshPostgresStaleReplayRevokesFamily(t *testing.T) {
	f := newRefreshPostgresFixture(t)
	first := f.rotate(t, f.token, "rotation-request-first")
	f.rotate(t, first["refresh_token"].(string), "rotation-request-second")
	req := f.request(t, f.token, "rotation-request-first")
	status, payload, _ := performJSON(t, f.h, http.MethodPost, "/api/v2/agent-sessions/refresh", req)
	if status != 401 || responseErrorCode(t, payload) != "REFRESH_REUSE_DETECTED" {
		t.Fatalf("stale replay=%d %#v", status, payload)
	}
	var active int64
	if err := f.db.Raw(`SELECT count(*) FROM agent_credential_sessions WHERE revoked_at IS NULL`).Scan(&active).Error; err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("stale replay left %d active sessions", active)
	}
}

func TestAgentRefreshPostgresExpiredSuccessorClassification(t *testing.T) {
	for _, tc := range []struct {
		name, mutation, rotation, challengeCode, refreshCode string
	}{
		{"same terminal rotation", "", "rotation-request-first", "REFRESH_EXPIRED", "REFRESH_EXPIRED"},
		{"different rotation", "", "rotation-request-other", "REFRESH_REUSE_DETECTED", "REFRESH_REUSE_DETECTED"},
		{"revoked successor", "UPDATE agent_credential_sessions SET revoked_at=1 WHERE session_id=2", "rotation-request-first", "REFRESH_INVALID", "REFRESH_REUSE_DETECTED"},
		{"rotated successor", "UPDATE agent_credential_sessions SET replaced_by_session_id=3 WHERE session_id=2", "rotation-request-first", "REFRESH_INVALID", "REFRESH_REUSE_DETECTED"},
		{"different successor principal", "UPDATE agent_credential_sessions SET principal_id=8 WHERE session_id=2", "rotation-request-first", "REFRESH_INVALID", "REFRESH_REUSE_DETECTED"},
		{"different successor family", "UPDATE agent_credential_sessions SET family_id='other-family' WHERE session_id=2", "rotation-request-first", "REFRESH_INVALID", "REFRESH_REUSE_DETECTED"},
		{"different rotation proof", "UPDATE agent_credential_sessions SET rotation_request_hash='other-hash' WHERE session_id=2", "rotation-request-first", "REFRESH_INVALID", "REFRESH_REUSE_DETECTED"},
		{"revoked principal", "UPDATE agent_principals SET revoked_at=1", "rotation-request-first", "REFRESH_INVALID", "REFRESH_INVALID"},
		{"inactive identity", "UPDATE agents SET identity_state='deleted'", "rotation-request-first", "REFRESH_INVALID", "REFRESH_INVALID"},
	} {
		for _, stage := range []string{"challenge", "refresh"} {
			t.Run(tc.name+"/"+stage, func(t *testing.T) {
				f := newRefreshPostgresFixture(t)
				f.rotate(t, f.token, "rotation-request-first")
				// Obtain a new nonce after the first successful proof consumed its
				// nonce, then move both deadlines past their offline window.
				req := f.request(t, f.token, "rotation-request-first")
				f.exec(t, `UPDATE agent_credential_sessions SET absolute_expires_at=0`)
				if tc.mutation != "" {
					f.exec(t, tc.mutation)
				}
				path, expected := "/api/v2/agent-sessions/refresh-challenges", tc.challengeCode
				var body interface{} = refreshChallengeRequest{RefreshToken: f.token, RotationRequestID: tc.rotation}
				if stage == "refresh" {
					path, expected = "/api/v2/agent-sessions/refresh", tc.refreshCode
					body = signedRefreshRequest(t, f.privateKey, f.token, tc.rotation, req.Nonce)
				}
				status, payload, _ := performJSON(t, f.h, http.MethodPost, path, body)
				if status != 401 || responseErrorCode(t, payload) != expected {
					t.Fatalf("expired successor=%d %#v, want %s", status, payload, expected)
				}
				var count int64
				if err := f.db.Raw(`SELECT count(*) FROM agent_credential_sessions`).Scan(&count).Error; err != nil || count != 2 {
					t.Fatalf("expired replay created a credential: count=%d err=%v", count, err)
				}
				if expected == "REFRESH_EXPIRED" {
					var active, extended int64
					if err := f.db.Raw(`SELECT count(*) FROM agent_credential_sessions WHERE revoked_at IS NULL`).Scan(&active).Error; err != nil || active != 1 {
						t.Fatalf("expiry revoked the terminal successor: active=%d err=%v", active, err)
					}
					if err := f.db.Raw(`SELECT count(*) FROM agent_credential_sessions WHERE absolute_expires_at<>0`).Scan(&extended).Error; err != nil || extended != 0 {
						t.Fatalf("expired replay extended a deadline: extended=%d err=%v", extended, err)
					}
				}
			})
		}
	}
}
