package consolev2

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
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

// This database regression verifies that recovery tombstones an unbound
// installation Agent without moving its draft, card, membership, or relations.
func TestHistoricalRecoveryAbandonsUnboundTemporaryAgent(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	idgen := &fixedIDGenerator{id: time.Now().UnixMilli() * 1000}
	svc, err := NewService(gdb, idgen, &config.Config{
		ConsoleV2BootstrapSecret: "abandon-broker-secret",
		ConsoleV2OTPPepper:       "abandon-otp-pepper",
		ConsoleV2PublicURL:       "https://console.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	redisServer := miniredis.RunT(t)
	svc.redisClient = redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	defer svc.redisClient.Close()
	mailbox := make(chan capturedEmail, 2)
	svc.emailSender = &captureEmailSender{sent: mailbox}
	svc.startEmailWorkers(1, 16)
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	svc.Register(h)

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKeyEncoded := base64.RawURLEncoding.EncodeToString(publicKey)
	draftJSON, err := json.Marshal(map[string]interface{}{
		"identity_card": map[string]interface{}{
			"agent_name": "Temporary Installation Agent",
			"bio":        "temporary draft must be removed",
		},
		"security_boundary": map[string]interface{}{
			"recurring_publish": false, "auto_reply_pm": false,
			"auto_comment": false, "show_add_friend": true,
		},
		"network_goal":   "temporary goal must not migrate",
		"intent_actions": []interface{}{},
	})
	if err != nil {
		t.Fatal(err)
	}
	entitlement := "abandon-recovery-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	status, grantPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/bootstrap-grants", map[string]interface{}{
		"entitlement_id":  entitlement,
		"idempotency_key": "grant-" + hashString(entitlement),
		"channel":         "test", "policy": "limited", "public_key": publicKeyEncoded,
	}, ut.Header{Key: "X-Bootstrap-Broker-Secret", Value: "abandon-broker-secret"})
	if status != http.StatusCreated {
		t.Fatalf("grant status=%d payload=%#v", status, grantPayload)
	}
	grantData := responseData(t, grantPayload)
	provisionReq := provisionRequest{
		BootstrapGrant: grantData["bootstrap_grant"].(string),
		IdempotencyKey: "provision-" + hashString(entitlement),
		Nonce:          grantData["nonce"].(string),
		PublicKey:      publicKeyEncoded,
		IssuedAt:       time.Now().UnixMilli(),
		AgentName:      "Temporary Installation Agent",
		Draft:          draftJSON,
	}
	provisionBytes, err := provisionTranscript(provisionReq)
	if err != nil {
		t.Fatal(err)
	}
	provisionReq.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, provisionBytes))
	status, provisionPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/agent-identities/provision", provisionReq,
		ut.Header{Key: "X-Client-Host", Value: "codex/1.0"})
	if status != http.StatusOK {
		t.Fatalf("provision status=%d payload=%#v", status, provisionPayload)
	}
	provisionData := responseData(t, provisionPayload)
	sourceID := mustParseInt64(t, provisionData["agent_id"].(string))
	principalID := mustParseInt64(t, provisionData["principal_id"].(string))
	oldAccess := provisionData["access_token"].(string)
	oldRefresh := provisionData["refresh_token"].(string)

	now := time.Now().UnixMilli()
	targetID, _ := idgen.NextID()
	historicalEmail := fmt.Sprintf("historical-abandon-%d@example.test", targetID)
	targetShortID, err := agentidentity.GenerateShortID()
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agents
		(agent_id, short_id, email, email_kind, agent_name, bio, created_at, updated_at, email_verified_at, profile_completed_at)
		VALUES (?, ?, ?, 'legacy_real', 'Historical Aurora', 'historical biography', ?, ?, ?, ?)`,
		targetID, targetShortID, historicalEmail, now-86400000, now-86400000, now-86400000, now-86400000).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_profiles
		(agent_id, status, country, profile_data, updated_at)
		VALUES (?, 0, 'SG', '{"seeking":["historical peers"]}'::jsonb, ?)`, targetID, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_cards
		(agent_id, public_card, private_card, schema_version, source_version, rebuild_fence,
		 card_version, public_card_version, generated_at, public_card_generated_at)
		VALUES (?, '{"agent_description":"historical description"}'::jsonb, '{}'::jsonb,
		1, 1, 1, 1, 1, ?, ?)`, targetID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_email_bindings
		(agent_id, normalized_email, normalization_version, verification_state, status, verified_at, created_at, updated_at)
		VALUES (?, ?, 1, 'verified', 'active', ?, ?, ?)`, targetID, historicalEmail, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_cards
		(agent_id, public_card, private_card, schema_version, source_version, rebuild_fence,
		 card_version, public_card_version, generated_at, public_card_generated_at)
		VALUES (?, '{"agent_description":"TEMP_CARD_MUST_BE_REMOVED"}'::jsonb, '{}'::jsonb,
		1, 1, 1, 1, 1, ?, ?)`, sourceID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_network_memberships (agent_id, joined_at)
		VALUES (?, ?) ON CONFLICT (agent_id) DO NOTHING`, sourceID, now).Error; err != nil {
		t.Fatal(err)
	}
	relationID, _ := idgen.NextID()
	if err := gdb.Exec(`INSERT INTO friend_requests
		(id, from_uid, to_uid, status, greeting, remark, created_at, updated_at)
		VALUES (?, ?, ?, 0, 'temporary source relation', '', ?, ?)`, relationID, sourceID, targetID, now, now).Error; err != nil {
		t.Fatal(err)
	}

	orphanPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	orphanPrincipalID, _ := idgen.NextID()
	if err := gdb.Exec(`INSERT INTO agent_principals
		(principal_id, agent_id, key_type, key_fingerprint, public_key, key_version, status, created_at, last_seen_at)
		VALUES (?, ?, 'ed25519-v1', ?, ?, 1, 'limited', ?, ?)`, orphanPrincipalID, sourceID,
		"orphan-"+hashString(entitlement), []byte(orphanPublicKey), now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_credential_sessions
		(principal_id, family_id, access_token_hash, refresh_token_hash, audience, scopes,
		 issued_at, expires_at, absolute_expires_at, last_seen_at)
		VALUES (?, ?, ?, ?, 'agent-api', '{}'::text[], ?, ?, ?, ?)`, orphanPrincipalID,
		"orphan-family-"+hashString(entitlement), "orphan-access-"+hashString(entitlement),
		"orphan-refresh-"+hashString(entitlement), now, now+3600000, now+7200000, now).Error; err != nil {
		t.Fatal(err)
	}

	createHandoff := func(nonce string) string {
		status, payload, _ := performJSON(t, h, http.MethodPost, "/api/v2/console/handoffs", map[string]interface{}{
			"browser_nonce": nonce, "client_capabilities": []string{"account_recovery_v1"},
		}, ut.Header{Key: "Authorization", Value: "Bearer " + oldAccess})
		if status != http.StatusCreated {
			t.Fatalf("handoff status=%d payload=%#v", status, payload)
		}
		parsed, err := url.Parse(responseData(t, payload)["handoff_url"].(string))
		if err != nil {
			t.Fatal(err)
		}
		return parsed.Query().Get("ticket")
	}
	browserNonce := strings.Repeat("a", 32)
	ticket := createHandoff(browserNonce)
	status, exchangePayload, cookies := performJSON(t, h, http.MethodPost, "/api/v2/console/handoffs/exchange", map[string]interface{}{
		"ticket": ticket, "browser_nonce": browserNonce,
	})
	if status != http.StatusOK {
		t.Fatalf("exchange status=%d payload=%#v", status, exchangePayload)
	}
	csrf := responseData(t, exchangePayload)["csrf_token"].(string)
	cookieHeader := cookiePair(cookies, consoleCookieName) + "; " + cookiePair(cookies, csrfCookieName)
	staleNonce := strings.Repeat("s", 32)
	staleTicket := createHandoff(staleNonce)

	orphanConsoleID := "orphan-console-" + hashString(entitlement)
	if err := gdb.Exec(`INSERT INTO console_v2_sessions
		(session_id, session_secret_hash, agent_id, principal_id, csrf_secret_hash, status, scopes,
		 issued_at, idle_expires_at, absolute_expires_at, last_seen_at, client_capabilities)
		VALUES (?, ?, ?, ?, ?, 'active', '{}'::text[], ?, ?, ?, ?, '{}'::text[])`, orphanConsoleID,
		"orphan-secret-"+hashString(entitlement), sourceID, orphanPrincipalID,
		"orphan-csrf-"+hashString(entitlement), now, now+3600000, now+7200000, now).Error; err != nil {
		t.Fatal(err)
	}

	status, challengePayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/account-email-bindings/challenges", createEmailChallengeRequest{
		Email: historicalEmail,
	}, ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusAccepted {
		t.Fatalf("email challenge status=%d payload=%#v", status, challengePayload)
	}
	var mail capturedEmail
	select {
	case mail = <-mailbox:
	case <-time.After(2 * time.Second):
		t.Fatal("email OTP not captured")
	}
	status, conflictPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/account-email-bindings/verify", verifyEmailRequest{
		ChallengeID: responseData(t, challengePayload)["challenge_id"].(string),
		Email:       historicalEmail, OTP: mail.otp,
	}, ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusConflict || responseErrorCode(t, conflictPayload) != "EMAIL_UNAVAILABLE" {
		t.Fatalf("expected recoverable conflict, status=%d payload=%#v", status, conflictPayload)
	}
	details := conflictPayload["error"].(map[string]interface{})["details"].(map[string]interface{})
	if details["reason"] != "existing_agent_recovery_available" || details["source_disposition"] != recoverySourceAbandon {
		t.Fatalf("unbound source was not classified for abandonment: %#v", details)
	}
	recoveryID := details["recovery_id"].(string)
	status, recoveryPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/account-recoveries/"+recoveryID+"/confirm", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusOK {
		t.Fatalf("recovery status=%d payload=%#v", status, recoveryPayload)
	}
	recoveryData := responseData(t, recoveryPayload)
	if recoveryData["agent_id"] != strconv.FormatInt(targetID, 10) ||
		recoveryData["principal_id"] != strconv.FormatInt(principalID, 10) ||
		recoveryData["source_disposition"] != recoverySourceAbandon || recoveryData["source_agent_abandoned"] != true {
		t.Fatalf("recovery returned wrong identity or disposition: %#v", recoveryData)
	}

	var sourceState, sourceEmail, sourceEmailKind string
	var sourceEmailVerifiedAt int64
	if err := gdb.Raw(`SELECT identity_state, email, email_kind, COALESCE(email_verified_at, 0)
		FROM agents WHERE agent_id = ?`, sourceID).
		Row().Scan(&sourceState, &sourceEmail, &sourceEmailKind, &sourceEmailVerifiedAt); err != nil {
		t.Fatal(err)
	}
	wantSourceEmail := fmt.Sprintf("recovered-%d@identity.invalid", sourceID)
	if sourceState != "recovered_temporary" || sourceEmail != wantSourceEmail ||
		sourceEmailKind != "internal_alias" || sourceEmailVerifiedAt != 0 {
		t.Fatalf("temporary source was not tombstoned: state=%q email=%q kind=%q verified_at=%d",
			sourceState, sourceEmail, sourceEmailKind, sourceEmailVerifiedAt)
	}
	if _, err := agentidentity.Get(context.Background(), gdb, sourceID); !errors.Is(err, agentidentity.ErrNotFound) {
		t.Fatalf("abandoned source lookup error=%v, want ErrNotFound", err)
	}

	var movedAgentID int64
	if err := gdb.Raw(`SELECT agent_id FROM agent_principals WHERE principal_id = ?`, principalID).Scan(&movedAgentID).Error; err != nil {
		t.Fatal(err)
	}
	var sourceProjectionCount, sourceBindingCount, relationCount int64
	if err := gdb.Raw(`SELECT
		(EXISTS (SELECT 1 FROM agent_onboarding_drafts WHERE agent_id = ?))::int +
		(EXISTS (SELECT 1 FROM agent_cards WHERE agent_id = ?))::int +
		(EXISTS (SELECT 1 FROM agent_network_memberships WHERE agent_id = ?))::int`,
		sourceID, sourceID, sourceID).Scan(&sourceProjectionCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw(`SELECT COUNT(*) FROM agent_email_bindings WHERE agent_id = ? AND status = 'active'`, sourceID).
		Scan(&sourceBindingCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw(`SELECT COUNT(*) FROM friend_requests WHERE id = ? AND from_uid = ? AND to_uid = ?`,
		relationID, sourceID, targetID).Scan(&relationCount).Error; err != nil {
		t.Fatal(err)
	}
	var orphanPrincipalStatus, orphanConsoleStatus string
	var orphanPrincipalRevokedAt, orphanCredentialRevokedAt, orphanConsoleRevokedAt int64
	if err := gdb.Raw(`SELECT status, COALESCE(revoked_at, 0) FROM agent_principals WHERE principal_id = ?`, orphanPrincipalID).
		Row().Scan(&orphanPrincipalStatus, &orphanPrincipalRevokedAt); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw(`SELECT COALESCE(revoked_at, 0) FROM agent_credential_sessions WHERE principal_id = ?`, orphanPrincipalID).
		Scan(&orphanCredentialRevokedAt).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw(`SELECT status, COALESCE(revoked_at, 0) FROM console_v2_sessions WHERE session_id = ?`, orphanConsoleID).
		Row().Scan(&orphanConsoleStatus, &orphanConsoleRevokedAt); err != nil {
		t.Fatal(err)
	}
	if movedAgentID != targetID || sourceProjectionCount != 0 || sourceBindingCount != 0 || relationCount != 1 ||
		orphanPrincipalStatus != "revoked" || orphanPrincipalRevokedAt == 0 || orphanCredentialRevokedAt == 0 ||
		orphanConsoleStatus != "revoked" || orphanConsoleRevokedAt == 0 {
		t.Fatalf("abandonment cleanup mismatch: moved_agent=%d projections=%d bindings=%d relations=%d orphan_principal=%q/%d credential_revoked=%d orphan_console=%q/%d",
			movedAgentID, sourceProjectionCount, sourceBindingCount, relationCount, orphanPrincipalStatus,
			orphanPrincipalRevokedAt, orphanCredentialRevokedAt, orphanConsoleStatus, orphanConsoleRevokedAt)
	}

	status, sessionPayload, _ := performJSON(t, h, http.MethodGet, "/api/v2/console/session", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != http.StatusOK || responseData(t, sessionPayload)["agent_id"] != strconv.FormatInt(targetID, 10) {
		t.Fatalf("console session did not move to historical Agent: status=%d payload=%#v", status, sessionPayload)
	}
	status, draftPayload, _ := performJSON(t, h, http.MethodGet, "/api/v2/agents/me/onboarding-draft", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != http.StatusOK {
		t.Fatalf("historical draft status=%d payload=%#v", status, draftPayload)
	}
	historicalDraft := responseData(t, draftPayload)["draft"].(map[string]interface{})["data"].(map[string]interface{})
	historicalIdentity := historicalDraft["identity_card"].(map[string]interface{})
	if historicalIdentity["agent_name"] != "Historical Aurora" || historicalIdentity["bio"] != "historical biography" {
		t.Fatalf("temporary draft leaked into historical Agent: %#v", historicalIdentity)
	}

	status, stalePayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/console/handoffs/exchange", map[string]interface{}{
		"ticket": staleTicket, "browser_nonce": staleNonce,
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("temporary source handoff survived: status=%d payload=%#v", status, stalePayload)
	}
	status, oldAccessPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/console/handoffs", map[string]interface{}{
		"browser_nonce": strings.Repeat("o", 32), "client_capabilities": []string{"account_recovery_v1"},
	}, ut.Header{Key: "Authorization", Value: "Bearer " + oldAccess})
	if status != http.StatusUnauthorized {
		t.Fatalf("pre-recovery access token survived: status=%d payload=%#v", status, oldAccessPayload)
	}

	rotationID := "abandon-rotation-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	status, refreshChallengePayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/agent-sessions/refresh-challenges", refreshChallengeRequest{
		RefreshToken: oldRefresh, RotationRequestID: rotationID,
	})
	if status != http.StatusCreated {
		t.Fatalf("refresh challenge status=%d payload=%#v", status, refreshChallengePayload)
	}
	refreshData := responseData(t, refreshChallengePayload)
	refreshReq := refreshAgentSessionRequest{
		RefreshToken: oldRefresh, RotationRequestID: rotationID,
		Nonce: refreshData["nonce"].(string), PublicKey: publicKeyEncoded, IssuedAt: time.Now().UnixMilli(),
	}
	refreshBytes, err := refreshTranscript(refreshReq)
	if err != nil {
		t.Fatal(err)
	}
	refreshReq.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, refreshBytes))
	status, refreshedPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/agent-sessions/refresh", refreshReq,
		ut.Header{Key: "X-CLI-Ver", Value: "0.0.35"})
	if status != http.StatusOK {
		t.Fatalf("CLI refresh status=%d payload=%#v", status, refreshedPayload)
	}
	refreshed := responseData(t, refreshedPayload)
	if refreshed["agent_id"] != strconv.FormatInt(targetID, 10) || refreshed["principal_id"] != strconv.FormatInt(principalID, 10) {
		t.Fatalf("CLI refresh did not adopt historical Agent: %#v", refreshed)
	}
}
