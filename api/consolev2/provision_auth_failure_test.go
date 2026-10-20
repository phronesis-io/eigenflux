package consolev2

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func newProvisionFailureFixture(t *testing.T) (*refreshPostgresFixture, *Service, provisionRequest) {
	t.Helper()
	f := newRefreshPostgresFixture(t)
	for _, query := range []string{
		`ALTER TABLE agent_principals ADD COLUMN key_type TEXT DEFAULT 'ed25519-v1'`,
		`ALTER TABLE agent_onboarding_v2 ADD COLUMN current_step SMALLINT DEFAULT 2`,
		`ALTER TABLE agents ADD COLUMN short_id TEXT DEFAULT 'AbCdE', ADD COLUMN agent_name TEXT DEFAULT 'Fixture', ADD COLUMN agent_name_en TEXT DEFAULT ''`,
		`CREATE TEMP TABLE agent_bootstrap_grants (jti_hash TEXT PRIMARY KEY, key_fingerprint TEXT, status TEXT, expires_at BIGINT, subject_agent_id BIGINT, consumed_at BIGINT, consumed_by_agent_id BIGINT, audience TEXT, channel TEXT, policy TEXT, entitlement_hash TEXT UNIQUE, request_id TEXT, created_at BIGINT)`,
		`CREATE TEMP TABLE agent_settings (agent_id BIGINT PRIMARY KEY)`,
	} {
		f.exec(t, query)
	}
	publicKey := f.privateKey.Public().(ed25519.PublicKey)
	req := provisionRequest{
		BootstrapGrant: "efbg_provision-secret", IdempotencyKey: "provision-request-fixture",
		Nonce: "efn_provision-secret", PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		IssuedAt: time.Now().UnixMilli(), AgentName: "EigenFlux Agent", ExpectedAgentID: "42", Draft: json.RawMessage(`{}`),
	}
	transcript, err := provisionTranscript(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.privateKey, transcript))
	now := time.Now().UnixMilli()
	f.exec(t, `INSERT INTO agent_bootstrap_grants (jti_hash,key_fingerprint,status,expires_at) VALUES (?,?,'issued',?)`, hashString(req.BootstrapGrant), fingerprint(publicKey), now+int64(grantTTL/time.Millisecond))
	f.exec(t, `INSERT INTO agent_signature_nonces (nonce_hash,key_fingerprint,domain,expires_at,created_at) VALUES (?,?,'provision',?,?)`, hashString(req.Nonce), fingerprint(publicKey), now+int64(grantTTL/time.Millisecond), now)
	return f, &Service{db: f.db, idgen: &fixedIDGenerator{id: 100}, otpPepper: "provision-test-pepper"}, req
}

func provisionFailureServer(svc *Service) *server.Hertz {
	h := server.New()
	ctx := context.WithValue(context.Background(), refreshContextKey{}, "present")
	h.POST("/api/v2/agent-identities/provision", func(_ context.Context, c *app.RequestContext) { svc.provision(ctx, c) })
	return h
}

func TestProvisionDatabaseFailuresRemainUnavailableAndRedacted(t *testing.T) {
	for _, tc := range []struct{ name, target, reason string }{
		{"identity lock", "pg_advisory_xact_lock", "identity_lock_failed"},
		{"database clock", "clock_timestamp()", "database_clock_failed"},
		{"receipt lookup", "SELECT p.agent_id, cs.principal_id", "receipt_lookup_failed"},
		{"grant lookup", "SELECT key_fingerprint, status", "bootstrap_grant_lookup_failed"},
		{"nonce lookup", "SELECT COUNT(*) FROM agent_signature_nonces", "nonce_lookup_failed"},
		{"principal lookup", "SELECT p.principal_id, p.agent_id, p.status", "principal_lookup_failed"},
		{"grant consume", "UPDATE agent_bootstrap_grants", "bootstrap_grant_update_failed"},
		{"nonce consume", "UPDATE agent_signature_nonces", "nonce_update_failed"},
		{"credential insert", "INSERT INTO agent_credential_sessions", "credential_insert_failed"},
		{"public identity", "SELECT short_id, agent_name", "public_identity_lookup_failed"},
		{"client report", "agent_settings", "client_report_failed"},
		{"transaction commit", "", "transaction_commit_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, svc, req := newProvisionFailureFixture(t)
			if tc.name == "transaction commit" {
				f.exec(t, `CREATE TEMP TABLE accepted_receipts (request_id TEXT PRIMARY KEY)`)
				f.exec(t, `ALTER TABLE agent_credential_sessions ADD CONSTRAINT accepted_provision_receipt FOREIGN KEY(rotation_request_id) REFERENCES accepted_receipts(request_id) DEFERRABLE INITIALLY DEFERRED`)
			}
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			svc.db = f.db.Session(&gorm.Session{Logger: gormlogger.New(log.New(&logs, "", 0), gormlogger.Config{LogLevel: gormlogger.Info})})
			injected, inHandler := false, true
			failQuery := func(query *gorm.DB) {
				if !inHandler {
					return
				}
				if query.Statement.Context.Value(refreshContextKey{}) != "present" {
					t.Error("provision authentication lost request context")
				}
				if tc.target != "" && (strings.Contains(query.Statement.SQL.String(), tc.target) || query.Statement.Table == tc.target) {
					injected = true
					query.AddError(&pgconn.PgError{Code: "08006", Message: "password=private provision-secret signature-secret SELECT private"})
				}
			}
			for _, callback := range []func() error{
				func() error {
					return svc.db.Callback().Raw().Before("gorm:raw").Register("test:provision_failure", failQuery)
				},
				func() error {
					return svc.db.Callback().Row().Before("gorm:row").Register("test:provision_failure", failQuery)
				},
				func() error {
					return svc.db.Callback().Query().After("gorm:query").Register("test:provision_failure", failQuery)
				},
			} {
				if err := callback(); err != nil {
					t.Fatal(err)
				}
			}
			status, payload, _ := performJSON(t, provisionFailureServer(svc), http.MethodPost, "/api/v2/agent-identities/provision", req)
			inHandler = false
			if status != http.StatusServiceUnavailable || responseErrorCode(t, payload) != "AGENT_AUTH_UNAVAILABLE" || (tc.target != "" && !injected) {
				t.Fatalf("provision failure=%d %#v injected=%t", status, payload, injected)
			}
			var record map[string]interface{}
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
				t.Fatalf("expected one sanitized failure log: %v %s", err, logs.String())
			}
			wantState := "08006"
			if tc.name == "transaction commit" {
				wantState = "23503"
			}
			if record["stage"] != "provision" || record["reason"] != tc.reason || record["sql_state"] != wantState {
				t.Fatalf("missing provision failure classification: %#v", record)
			}
			for _, secret := range []string{"password=", "provision-secret", "signature-secret", "SELECT", hashString(req.BootstrapGrant), hashString(req.Nonce), req.Signature} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("authentication log leaked %q", secret)
				}
			}
			if tc.name != "public identity" && tc.name != "client report" {
				// Failed proof consumption or COMMIT must roll back all auth writes.
				var count int64
				if err := f.db.Raw(`SELECT count(*) FROM agent_credential_sessions`).Scan(&count).Error; err != nil || count != 1 {
					t.Fatalf("failed provision changed credentials: count=%d err=%v", count, err)
				}
				var consumed int64
				if err := f.db.Raw(`SELECT count(*) FROM agent_bootstrap_grants WHERE consumed_at IS NOT NULL`).Scan(&consumed).Error; err != nil || consumed != 0 {
					t.Fatalf("failed provision consumed grant: count=%d err=%v", consumed, err)
				}
				if err := f.db.Raw(`SELECT count(*) FROM agent_signature_nonces WHERE domain='provision' AND consumed_at IS NOT NULL`).Scan(&consumed).Error; err != nil || consumed != 0 {
					t.Fatalf("failed provision consumed nonce: count=%d err=%v", consumed, err)
				}
			}
		})
	}
}

func TestProvisionInvalidProofConsumptionRemainsUnauthorized(t *testing.T) {
	for _, target := range []string{"UPDATE agent_bootstrap_grants", "UPDATE agent_signature_nonces"} {
		t.Run(target, func(t *testing.T) {
			f, svc, req := newProvisionFailureFixture(t)
			if err := f.db.Callback().Raw().Before("gorm:raw").Register("test:provision_consumed", func(query *gorm.DB) {
				if strings.Contains(query.Statement.SQL.String(), target) {
					query.Statement.SQL.Reset()
					query.Statement.SQL.WriteString("UPDATE agent_signature_nonces SET consumed_at=0 WHERE 1=0")
					query.Statement.Vars = nil
				}
			}); err != nil {
				t.Fatal(err)
			}
			status, payload, _ := performJSON(t, provisionFailureServer(svc), http.MethodPost, "/api/v2/agent-identities/provision", req)
			if status != http.StatusUnauthorized || responseErrorCode(t, payload) != "BOOTSTRAP_PROOF_REJECTED" {
				t.Fatalf("invalid proof consumption changed classification=%d %#v", status, payload)
			}
		})
	}
}

func TestProvisionReceiptConflictRemainsConflict(t *testing.T) {
	f, svc, req := newProvisionFailureFixture(t)
	f.exec(t, `UPDATE agent_credential_sessions SET rotation_request_id=?,rotation_request_hash='different-request'`, "provision:"+req.IdempotencyKey)
	status, payload, _ := performJSON(t, provisionFailureServer(svc), http.MethodPost, "/api/v2/agent-identities/provision", req)
	if status != http.StatusConflict || responseErrorCode(t, payload) != "PROVISION_IDEMPOTENCY_CONFLICT" {
		t.Fatalf("receipt conflict changed classification=%d %#v", status, payload)
	}
}
