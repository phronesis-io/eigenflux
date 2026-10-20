package consolev2

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/jackc/pgx/v5/pgconn"
	redis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func newExistingIdentityRecoveryFixture(t *testing.T, publicRegistration bool) (*refreshPostgresFixture, *Service, *server.Hertz, existingIdentityRecoveryChallengeRequest) {
	t.Helper()
	f, svc, initial := newProvisionFailureFixture(t)
	f.exec(t, `ALTER TABLE agent_settings ADD COLUMN device_name TEXT, ADD COLUMN updated_at BIGINT`)
	f.exec(t, `INSERT INTO agent_settings (agent_id) VALUES (42)`)
	cache := miniredis.RunT(t)
	svc.redisClient = redis.NewClient(&redis.Options{Addr: cache.Addr()})
	t.Cleanup(func() { svc.redisClient.Close() })
	svc.bootstrapSecret = "" // Existing-key recovery does not require the new-registration broker secret.
	svc.enablePublicRegistration = publicRegistration
	svc.registrationLimits = registrationRateLimits{Window: time.Minute, IP: 1000, Subnet: 1000, PublicKey: 1000, Global: 1000}
	h := server.New()
	svc.Register(h)
	return f, svc, h, existingIdentityRecoveryChallengeRequest{
		PublicKey: initial.PublicKey, IdempotencyKey: "existing-recovery-request", ExpectedAgentID: "42",
	}
}

func existingIdentityProvisionProof(t *testing.T, f *refreshPostgresFixture, challenge map[string]interface{}, expectedAgentID string) provisionRequest {
	t.Helper()
	req := provisionRequest{
		BootstrapGrant: challenge["bootstrap_grant"].(string), Nonce: challenge["nonce"].(string),
		IdempotencyKey: "provision-existing-recovery", ExpectedAgentID: expectedAgentID,
		PublicKey: base64.RawURLEncoding.EncodeToString(f.privateKey.Public().(ed25519.PublicKey)),
		IssuedAt:  time.Now().UnixMilli(), AgentName: "EigenFlux Agent",
	}
	transcript, err := provisionTranscript(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.privateKey, transcript))
	return req
}

func TestExistingIdentityRecoveryPostgresIndependentOfPublicRegistration(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, principalStatus := range []string{"active", "limited"} {
			t.Run(map[bool]string{false: "closed", true: "open"}[enabled]+"/"+principalStatus, func(t *testing.T) {
				f, _, h, req := newExistingIdentityRecoveryFixture(t, enabled)
				f.exec(t, `UPDATE agent_principals SET status=?`, principalStatus)
				status, payload, _ := performJSON(t, h, http.MethodPost, "/api/v2/agent-identities/recovery-challenges", req)
				if status != 201 {
					t.Fatalf("existing-key challenge=%d %#v", status, payload)
				}
				challenge := responseData(t, payload)
				status, payload, _ = performJSON(t, h, http.MethodPost, "/api/v2/agent-identities/recovery-challenges", req)
				if status != 201 || responseData(t, payload)["bootstrap_grant"] != challenge["bootstrap_grant"] || responseData(t, payload)["nonce"] != challenge["nonce"] {
					t.Fatalf("challenge retry lost its receipt=%d %#v", status, payload)
				}
				var grant struct {
					SubjectAgentID int64
					Channel        string
				}
				if err := f.db.Raw(`SELECT subject_agent_id,channel FROM agent_bootstrap_grants WHERE jti_hash=?`, hashString(challenge["bootstrap_grant"].(string))).Scan(&grant).Error; err != nil || grant.SubjectAgentID != 42 || grant.Channel != existingIdentityRecoveryChannel {
					t.Fatalf("recovery grant was not restricted to the existing identity: %+v %v", grant, err)
				}
				proof := existingIdentityProvisionProof(t, f, challenge, req.ExpectedAgentID)
				for replay := 0; replay < 2; replay++ {
					status, payload, _ = performJSON(t, h, http.MethodPost, "/api/v2/agent-identities/provision", proof)
					if status != 200 {
						t.Fatalf("existing-key provision=%d %#v", status, payload)
					}
					data := responseData(t, payload)
					if data["agent_id"] != "42" || data["principal_id"] != "7" || data["created"] != false {
						t.Fatalf("existing-key recovery changed identity: %#v", data)
					}
				}
				for table, want := range map[string]int64{"agents": 1, "agent_principals": 1, "agent_credential_sessions": 2} {
					var count int64
					if err := f.db.Table(table).Count(&count).Error; err != nil || count != want {
						t.Fatalf("recovery changed %s: count=%d want=%d err=%v", table, count, want, err)
					}
				}
				if !enabled {
					status := ut.PerformRequest(h.Engine, http.MethodPost, "/api/v2/agent-identities/registration-challenges", nil).Result().StatusCode()
					if status != 404 {
						t.Fatalf("recovery opened new registration: %d", status)
					}
				}
			})
		}
	}
}

func TestExistingIdentityRecoveryPostgresRejectsUnknownAndInactiveKeys(t *testing.T) {
	for _, tc := range []struct{ name, mutation, expectedID string }{
		{"unknown key", "UPDATE agent_principals SET key_fingerprint='different-key'", "42"},
		{"wrong account", "", "43"},
		{"revoked status", "UPDATE agent_principals SET status='revoked'", "42"},
		{"suspended status", "UPDATE agent_principals SET status='suspended'", "42"},
		{"revoked timestamp", "UPDATE agent_principals SET revoked_at=1", "42"},
		{"inactive identity", "UPDATE agents SET identity_state='deleted'", "42"},
		{"recovered identity", "UPDATE agents SET identity_state='recovered_temporary'", "42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, h, req := newExistingIdentityRecoveryFixture(t, false)
			if tc.mutation != "" {
				f.exec(t, tc.mutation)
			}
			req.ExpectedAgentID = tc.expectedID
			status, payload, _ := performJSON(t, h, http.MethodPost, "/api/v2/agent-identities/recovery-challenges", req)
			if status != 401 || responseErrorCode(t, payload) != "RECOVERY_IDENTITY_INVALID" {
				t.Fatalf("invalid existing identity=%d %#v", status, payload)
			}
			var count int64
			if err := f.db.Raw(`SELECT count(*) FROM agent_bootstrap_grants WHERE channel=?`, existingIdentityRecoveryChannel).Scan(&count).Error; err != nil || count != 0 {
				t.Fatalf("invalid identity received a recovery grant: count=%d err=%v", count, err)
			}
		})
	}
}

func TestExistingIdentityRecoveryPostgresRechecksIdentityAtProvision(t *testing.T) {
	for _, tc := range []struct{ name, mutation, expectedID string }{
		{"principal revoked", "UPDATE agent_principals SET status='revoked',revoked_at=1", "42"},
		{"principal revoked timestamp", "UPDATE agent_principals SET revoked_at=1", "42"},
		{"principal suspended", "UPDATE agent_principals SET status='suspended'", "42"},
		{"principal removed", "DELETE FROM agent_principals", "42"},
		{"principal rebound", "UPDATE agent_principals SET agent_id=43", "42"},
		{"identity inactive", "UPDATE agents SET identity_state='deleted'", "42"},
		{"expected identity omitted", "", ""},
		{"expected identity changed", "", "43"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, h, req := newExistingIdentityRecoveryFixture(t, false)
			status, payload, _ := performJSON(t, h, http.MethodPost, "/api/v2/agent-identities/recovery-challenges", req)
			if status != 201 {
				t.Fatalf("challenge=%d %#v", status, payload)
			}
			proof := existingIdentityProvisionProof(t, f, responseData(t, payload), tc.expectedID)
			if tc.mutation != "" {
				f.exec(t, tc.mutation)
			}
			status, payload, _ = performJSON(t, h, http.MethodPost, "/api/v2/agent-identities/provision", proof)
			if status != 401 || responseErrorCode(t, payload) != "BOOTSTRAP_PROOF_REJECTED" {
				t.Fatalf("changed identity accepted during provision=%d %#v", status, payload)
			}
			var count int64
			if err := f.db.Raw(`SELECT count(*) FROM agent_credential_sessions`).Scan(&count).Error; err != nil || count != 1 {
				t.Fatalf("rejected recovery issued credentials: count=%d err=%v", count, err)
			}
			if err := f.db.Raw(`SELECT count(*) FROM agent_bootstrap_grants WHERE channel=? AND consumed_at IS NOT NULL`, existingIdentityRecoveryChannel).Scan(&count).Error; err != nil || count != 0 {
				t.Fatalf("rejected recovery consumed its grant: count=%d err=%v", count, err)
			}
		})
	}
}

func TestExistingIdentityRecoveryPostgresFailuresStayUnavailableAndRedacted(t *testing.T) {
	for _, tc := range []struct{ name, target, reason string }{
		{"identity lookup", "SELECT p.principal_id FROM agent_principals p", "recovery_identity_lookup_failed"},
		{"grant insert", "INSERT INTO agent_bootstrap_grants", "recovery_grant_failed"},
		{"nonce insert", "INSERT INTO agent_signature_nonces", "recovery_grant_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, svc, h, req := newExistingIdentityRecoveryFixture(t, false)
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			inject := func(query *gorm.DB) {
				if strings.Contains(query.Statement.SQL.String(), tc.target) {
					query.AddError(&pgconn.PgError{Code: "08006", Message: "password=private signature-secret SELECT private"})
				}
			}
			if err := f.db.Callback().Raw().Before("gorm:raw").Register("test:recovery_failure", inject); err != nil {
				t.Fatal(err)
			}
			if err := f.db.Callback().Row().Before("gorm:row").Register("test:recovery_failure", inject); err != nil {
				t.Fatal(err)
			}
			status, payload, _ := performJSON(t, h, http.MethodPost, "/api/v2/agent-identities/recovery-challenges", req)
			if status != 503 || responseErrorCode(t, payload) != "AGENT_AUTH_UNAVAILABLE" {
				t.Fatalf("recovery infrastructure failure=%d %#v", status, payload)
			}
			var record map[string]interface{}
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil || record["reason"] != tc.reason || record["sql_state"] != "08006" {
				t.Fatalf("missing sanitized recovery cause: %s err=%v", &logs, err)
			}
			for _, secret := range []string{"password=", "signature-secret", "SELECT", svc.otpPepper} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("recovery log leaked %q", secret)
				}
			}
		})
	}
}

func TestExistingIdentityRecoveryPostgresLimiterFailsClosed(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable", true: "rate limited"}[limited], func(t *testing.T) {
			_, svc, h, req := newExistingIdentityRecoveryFixture(t, false)
			expectedStatus, expectedCode := 503, "AGENT_AUTH_UNAVAILABLE"
			if limited {
				svc.registrationLimits.PublicKey = 1
				status, payload, _ := performJSON(t, h, http.MethodPost, "/api/v2/agent-identities/recovery-challenges", req)
				if status != 201 {
					t.Fatalf("initial challenge=%d %#v", status, payload)
				}
				expectedStatus, expectedCode = 429, "RECOVERY_RATE_LIMITED"
			} else {
				svc.redisClient.Close()
			}
			status, payload, _ := performJSON(t, h, http.MethodPost, "/api/v2/agent-identities/recovery-challenges", req)
			if status != expectedStatus || responseErrorCode(t, payload) != expectedCode {
				t.Fatalf("recovery limiter=%d %#v", status, payload)
			}
		})
	}
}
