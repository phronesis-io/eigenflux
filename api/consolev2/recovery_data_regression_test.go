package consolev2

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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

// This database regression verifies that switching away from an email-bound
// source preserves the formal account and allows switching back later.
func TestHistoricalRecoveryPreservesBoundSourceAgent(t *testing.T) {
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
		ConsoleV2BootstrapSecret: "review-broker-secret",
		ConsoleV2OTPPepper:       "review-otp-pepper",
		ConsoleV2PublicURL:       "https://console.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	redisServer := miniredis.RunT(t)
	svc.redisClient = redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	defer svc.redisClient.Close()
	mailbox := make(chan capturedEmail, 4)
	svc.emailSender = &captureEmailSender{sent: mailbox}
	svc.startEmailWorkers(1, 16)
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	svc.Register(h)

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKeyEncoded := base64.RawURLEncoding.EncodeToString(publicKey)
	draftJSON, _ := json.Marshal(map[string]interface{}{
		"identity_card": map[string]interface{}{
			"agent_name": "Disposable Installation Agent",
			"bio":        "temporary prefill must disappear",
		},
		"security_boundary": map[string]interface{}{
			"recurring_publish": false, "auto_reply_pm": false,
			"auto_comment": false, "show_add_friend": true,
		},
		"network_goal":   "temporary network goal",
		"intent_actions": []interface{}{},
	})
	entitlement := "review-recovery-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	status, grantPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/bootstrap-grants", map[string]interface{}{
		"entitlement_id": entitlement, "idempotency_key": "grant-" + hashString(entitlement),
		"channel": "review", "policy": "limited", "public_key": publicKeyEncoded,
	}, ut.Header{Key: "X-Bootstrap-Broker-Secret", Value: "review-broker-secret"})
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
		AgentName:      "Disposable Installation Agent",
		Draft:          draftJSON,
	}
	provisionTranscriptBytes, err := provisionTranscript(provisionReq)
	if err != nil {
		t.Fatal(err)
	}
	provisionReq.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, provisionTranscriptBytes))
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
	historicalEmail := fmt.Sprintf("historical-review-%d@example.test", targetID)
	historicalShortID, err := agentidentity.GenerateShortID()
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agents
		(agent_id, short_id, email, email_kind, agent_name, bio, created_at, updated_at, email_verified_at, profile_completed_at)
		VALUES (?, ?, ?, 'legacy_real', 'Historical Atlas', 'historical card bio', ?, ?, ?, ?)`,
		targetID, historicalShortID, historicalEmail, now-86400000, now-86400000, now-86400000, now-86400000).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_profiles
		(agent_id, status, country, profile_data, updated_at)
		VALUES (?, 0, 'SG', '{"seeking":["historical partners"],"offering":["historical research"]}'::jsonb, ?)`,
		targetID, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_cards
		(agent_id, public_card, private_card, schema_version, source_version, rebuild_fence,
		 card_version, public_card_version, generated_at, public_card_generated_at)
		VALUES (?, '{"agent_description":"historical public description","seeking":["historical partners"]}'::jsonb,
		'{"current_focus":["historical focus"]}'::jsonb, 1, 3, 3, 4, 4, ?, ?)`, targetID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_email_bindings
		(agent_id, normalized_email, normalization_version, verification_state, status, verified_at, created_at, updated_at)
		VALUES (?, ?, 1, 'verified', 'active', ?, ?, ?)`, targetID, historicalEmail, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_context_revisions
		(agent_id, revision, compiled_context, schema_version, generated_at)
		VALUES (?, 1, '{"owner":"historical"}'::jsonb, 1, ?)`, targetID, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_context_heads (agent_id, current_revision, active_revision, updated_at)
		VALUES (?, 1, 1, ?)`, targetID, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_onboarding_v2
		(agent_id, state, current_step, revision, active_context_revision, completed_at, created_at, updated_at)
		VALUES (?, 'completed', 5, 7, 1, ?, ?, ?)`, targetID, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_onboarding_drafts
		(agent_id, revision, draft_data, field_provenance, actor_type, request_id, created_at)
		VALUES (?, 7,
		'{"identity_card":{"agent_name":"Historical Atlas","bio":"historical card bio","agent_description":"historical public description","seeking":["historical partners"],"current_focus":["historical focus"]},"security_boundary":{"recurring_publish":false,"auto_reply_pm":false,"auto_comment":false,"show_add_friend":true},"network_goal":"historical goal","intent_actions":[]}'::jsonb,
		'{}'::jsonb, 'human_edit', 'historical-confirmed', ?)`, targetID, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_feed_v2_settings
		(agent_id, poll_interval_seconds, explicitly_set, updated_at) VALUES (?, 600, false, ?)`, targetID, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_cards
		(agent_id, public_card, private_card, schema_version, source_version, rebuild_fence,
		 card_version, public_card_version, generated_at, public_card_generated_at)
		VALUES (?, '{"agent_description":"TEMP_CARD_MUST_DISAPPEAR"}'::jsonb, '{}'::jsonb,
		1, 1, 1, 1, 1, ?, ?)`, sourceID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_network_memberships (agent_id, joined_at)
		VALUES (?, ?) ON CONFLICT (agent_id) DO NOTHING`, sourceID, now).Error; err != nil {
		t.Fatal(err)
	}

	createHandoff := func(nonce string) (string, map[string]interface{}) {
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
		return parsed.Query().Get("ticket"), payload
	}
	browserNonce := strings.Repeat("r", 32)
	ticket, _ := createHandoff(browserNonce)
	status, exchangePayload, cookies := performJSON(t, h, http.MethodPost, "/api/v2/console/handoffs/exchange", map[string]interface{}{
		"ticket": ticket, "browser_nonce": browserNonce,
	})
	if status != http.StatusOK {
		t.Fatalf("exchange status=%d payload=%#v", status, exchangePayload)
	}
	exchangeData := responseData(t, exchangePayload)
	csrf := exchangeData["csrf_token"].(string)
	cookieHeader := cookiePair(cookies, consoleCookieName) + "; " + cookiePair(cookies, csrfCookieName)

	discardedEmail := fmt.Sprintf("discarded-%d@example.test", sourceID)
	if err := gdb.Exec(`UPDATE agents SET email = ?, email_kind = 'v2_bound', email_verified_at = ?, updated_at = ?
		WHERE agent_id = ?`, discardedEmail, now, now, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_email_bindings
		(agent_id, normalized_email, normalization_version, verification_state, status, verified_at, created_at, updated_at)
		VALUES (?, ?, 1, 'verified', 'active', ?, ?, ?)`, sourceID, discardedEmail, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_context_revisions
		(agent_id, revision, compiled_context, schema_version, generated_at)
		VALUES (?, 1, '{"owner":"bound source"}'::jsonb, 1, ?)
		ON CONFLICT (agent_id, revision) DO NOTHING`, sourceID, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_context_heads
		(agent_id, current_revision, active_revision, updated_at)
		VALUES (?, 1, 1, ?)
		ON CONFLICT (agent_id) DO UPDATE SET active_revision = 1, updated_at = EXCLUDED.updated_at`, sourceID, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`UPDATE agent_onboarding_v2
		SET state = 'completed', current_step = 5, active_context_revision = 1, completed_at = ?, updated_at = ? WHERE agent_id = ?`,
		now, now, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	discardedRequestID, _ := idgen.NextID()
	if err := gdb.Exec(`INSERT INTO friend_requests
		(id, from_uid, to_uid, status, greeting, remark, created_at, updated_at)
		VALUES (?, ?, ?, 0, 'discarded source activity', '', ?, ?)`,
		discardedRequestID, sourceID, targetID, now, now).Error; err != nil {
		t.Fatal(err)
	}

	staleNonce := strings.Repeat("s", 32)
	staleTicket, _ := createHandoff(staleNonce)
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
	errorData := conflictPayload["error"].(map[string]interface{})
	details := errorData["details"].(map[string]interface{})
	if details["reason"] != "existing_agent_recovery_available" {
		t.Fatalf("unexpected recovery details: %#v", details)
	}
	if details["source_disposition"] != recoverySourcePreserve {
		t.Fatalf("bound source must be preserved: %#v", details)
	}
	recoveryID := details["recovery_id"].(string)
	candidate := details["candidate"].(map[string]interface{})
	if candidate["display_name"] != "Historical Atlas" {
		t.Fatalf("wrong historical candidate: %#v", candidate)
	}

	status, recoveryPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/account-recoveries/"+recoveryID+"/confirm", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusOK {
		t.Fatalf("recovery status=%d payload=%#v", status, recoveryPayload)
	}
	recoveryData := responseData(t, recoveryPayload)
	if recoveryData["agent_id"] != strconv.FormatInt(targetID, 10) || recoveryData["principal_id"] != strconv.FormatInt(principalID, 10) {
		t.Fatalf("recovery returned wrong identity: %#v", recoveryData)
	}
	if recoveryData["source_agent_abandoned"] != false || recoveryData["source_disposition"] != recoverySourcePreserve {
		t.Fatalf("bound source was treated as temporary: %#v", recoveryData)
	}
	status, replayPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/account-recoveries/"+recoveryID+"/confirm", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusOK || responseData(t, replayPayload)["agent_id"] != strconv.FormatInt(targetID, 10) {
		t.Fatalf("recovery idempotency failed: status=%d payload=%#v", status, replayPayload)
	}

	var sourceState, targetState string
	if err := gdb.Raw(`SELECT identity_state FROM agents WHERE agent_id = ?`, sourceID).Scan(&sourceState).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw(`SELECT identity_state FROM agents WHERE agent_id = ?`, targetID).Scan(&targetState).Error; err != nil {
		t.Fatal(err)
	}
	if sourceState != "active" || targetState != "active" {
		t.Fatalf("identity states source=%q target=%q", sourceState, targetState)
	}
	var sourceCanonicalEmail, sourceEmailKind string
	if err := gdb.Raw(`SELECT email, email_kind FROM agents WHERE agent_id = ?`, sourceID).
		Row().Scan(&sourceCanonicalEmail, &sourceEmailKind); err != nil {
		t.Fatal(err)
	}
	if sourceCanonicalEmail != discardedEmail || sourceEmailKind != "v2_bound" {
		t.Fatalf("formal Agent lost its login email: email=%q kind=%q", sourceCanonicalEmail, sourceEmailKind)
	}
	var movedAgentID int64
	if err := gdb.Raw(`SELECT agent_id FROM agent_principals WHERE principal_id = ?`, principalID).Scan(&movedAgentID).Error; err != nil || movedAgentID != targetID {
		t.Fatalf("principal did not move to target: agent=%d err=%v", movedAgentID, err)
	}
	var sourcePrincipalCount, sourceBindingCount, targetBindingCount, sourceProjectionCount, sourceRelationCount, refreshRequiredCount int64
	if err := gdb.Raw(`SELECT COUNT(*) FROM agent_principals WHERE agent_id = ?`, sourceID).Scan(&sourcePrincipalCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw(`SELECT COUNT(*) FROM agent_email_bindings WHERE agent_id = ? AND status = 'active'`, sourceID).Scan(&sourceBindingCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw(`SELECT COUNT(*) FROM agent_email_bindings WHERE agent_id = ? AND status = 'active'`, targetID).Scan(&targetBindingCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw(`SELECT
		(EXISTS (SELECT 1 FROM agent_onboarding_drafts WHERE agent_id = ?))::int +
		(EXISTS (SELECT 1 FROM agent_cards WHERE agent_id = ?))::int +
		(EXISTS (SELECT 1 FROM agent_network_memberships WHERE agent_id = ?))::int`,
		sourceID, sourceID, sourceID).Scan(&sourceProjectionCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw(`SELECT COUNT(*) FROM friend_requests WHERE id = ? AND from_uid = ? AND to_uid = ?`,
		discardedRequestID, sourceID, targetID).Scan(&sourceRelationCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw(`SELECT COUNT(*) FROM agent_credential_sessions
		WHERE principal_id = ? AND revoked_at IS NULL AND access_refresh_required = TRUE`, principalID).Scan(&refreshRequiredCount).Error; err != nil {
		t.Fatal(err)
	}
	if sourcePrincipalCount != 0 || sourceBindingCount != 1 || targetBindingCount != 1 || sourceProjectionCount != 3 || sourceRelationCount != 1 || refreshRequiredCount == 0 {
		t.Fatalf("formal accounts were not preserved: principals=%d source_bindings=%d target_bindings=%d projections=%d relations=%d refresh_required=%d",
			sourcePrincipalCount, sourceBindingCount, targetBindingCount, sourceProjectionCount, sourceRelationCount, refreshRequiredCount)
	}
	if _, err := agentidentity.Get(context.Background(), gdb, sourceID); err != nil {
		t.Fatalf("preserved formal Agent is no longer discoverable: %v", err)
	}

	status, sessionPayload, _ := performJSON(t, h, http.MethodGet, "/api/v2/console/session", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != http.StatusOK {
		t.Fatalf("recovered console session status=%d payload=%#v", status, sessionPayload)
	}
	sessionData := responseData(t, sessionPayload)
	if sessionData["agent_id"] != strconv.FormatInt(targetID, 10) || sessionData["agent_name"] != "Historical Atlas" || sessionData["email"] != historicalEmail {
		t.Fatalf("console did not switch to historical Agent: %#v", sessionData)
	}
	status, draftPayload, _ := performJSON(t, h, http.MethodGet, "/api/v2/agents/me/onboarding-draft", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != http.StatusOK {
		t.Fatalf("historical draft status=%d payload=%#v", status, draftPayload)
	}
	historicalDraft := responseData(t, draftPayload)["draft"].(map[string]interface{})
	historicalData := historicalDraft["data"].(map[string]interface{})
	historicalIdentity := historicalData["identity_card"].(map[string]interface{})
	if historicalIdentity["agent_name"] != "Historical Atlas" || historicalIdentity["bio"] != "historical card bio" {
		t.Fatalf("temporary draft leaked into recovered console: %#v", historicalIdentity)
	}

	status, staleExchangePayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/console/handoffs/exchange", map[string]interface{}{
		"ticket": staleTicket, "browser_nonce": staleNonce,
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("moved principal's stale handoff survived: status=%d payload=%#v", status, staleExchangePayload)
	}
	status, oldTokenPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/console/handoffs", map[string]interface{}{
		"browser_nonce": strings.Repeat("o", 32), "client_capabilities": []string{"account_recovery_v1"},
	}, ut.Header{Key: "Authorization", Value: "Bearer " + oldAccess})
	if status != http.StatusUnauthorized {
		t.Fatalf("old source access token survived recovery: status=%d payload=%#v", status, oldTokenPayload)
	}

	provisionReq.IssuedAt = time.Now().UnixMilli()
	provisionTranscriptBytes, err = provisionTranscript(provisionReq)
	if err != nil {
		t.Fatal(err)
	}
	provisionReq.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, provisionTranscriptBytes))
	status, reprovisionPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/agent-identities/provision", provisionReq,
		ut.Header{Key: "X-Client-Host", Value: "codex/1.0"})
	if status != http.StatusOK {
		t.Fatalf("same-home reprovision status=%d payload=%#v", status, reprovisionPayload)
	}
	reprovisionData := responseData(t, reprovisionPayload)
	if reprovisionData["agent_id"] != strconv.FormatInt(targetID, 10) || reprovisionData["created"] != false {
		t.Fatalf("same key did not retain the selected Agent: %#v", reprovisionData)
	}

	rotationID := "review-rotation-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	status, refreshChallengePayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/agent-sessions/refresh-challenges", refreshChallengeRequest{
		RefreshToken: oldRefresh, RotationRequestID: rotationID,
	})
	if status != http.StatusCreated {
		t.Fatalf("refresh challenge status=%d payload=%#v", status, refreshChallengePayload)
	}
	refreshChallengeData := responseData(t, refreshChallengePayload)
	refreshReq := refreshAgentSessionRequest{
		RefreshToken: oldRefresh, RotationRequestID: rotationID,
		Nonce: refreshChallengeData["nonce"].(string), PublicKey: publicKeyEncoded,
		IssuedAt: time.Now().UnixMilli(),
	}
	refreshTranscriptBytes, err := refreshTranscript(refreshReq)
	if err != nil {
		t.Fatal(err)
	}
	refreshReq.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, refreshTranscriptBytes))
	status, oldCLIPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/agent-sessions/refresh", refreshReq,
		ut.Header{Key: "X-CLI-Ver", Value: "0.0.34"})
	if status != http.StatusUpgradeRequired || responseErrorCode(t, oldCLIPayload) != "CLI_UPGRADE_REQUIRED" {
		t.Fatalf("old CLI was not blocked: status=%d payload=%#v", status, oldCLIPayload)
	}
	status, refreshedPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/agent-sessions/refresh", refreshReq,
		ut.Header{Key: "X-CLI-Ver", Value: "0.0.35"})
	if status != http.StatusOK {
		t.Fatalf("CLI 0.0.35 refresh status=%d payload=%#v", status, refreshedPayload)
	}
	refreshedData := responseData(t, refreshedPayload)
	if refreshedData["agent_id"] != strconv.FormatInt(targetID, 10) || refreshedData["principal_id"] != strconv.FormatInt(principalID, 10) {
		t.Fatalf("CLI refresh did not adopt historical Agent: %#v", refreshedData)
	}
	newAccess := refreshedData["access_token"].(string)
	status, newTokenPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/console/handoffs", map[string]interface{}{
		"browser_nonce": strings.Repeat("n", 32), "client_capabilities": []string{"account_recovery_v1"},
	}, ut.Header{Key: "Authorization", Value: "Bearer " + newAccess})
	if status != http.StatusCreated {
		t.Fatalf("refreshed historical credential unusable: status=%d payload=%#v", status, newTokenPayload)
	}

	status, switchBackChallengePayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/account-email-bindings/challenges", createEmailChallengeRequest{
		Email: discardedEmail,
	}, ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusAccepted {
		t.Fatalf("switch-back challenge status=%d payload=%#v", status, switchBackChallengePayload)
	}
	select {
	case mail = <-mailbox:
	case <-time.After(2 * time.Second):
		t.Fatal("switch-back OTP not captured")
	}
	status, switchBackConflictPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/account-email-bindings/verify", verifyEmailRequest{
		ChallengeID: responseData(t, switchBackChallengePayload)["challenge_id"].(string),
		Email:       discardedEmail,
		OTP:         mail.otp,
	}, ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusConflict || responseErrorCode(t, switchBackConflictPayload) != "EMAIL_UNAVAILABLE" {
		t.Fatalf("expected switch-back recovery conflict, status=%d payload=%#v", status, switchBackConflictPayload)
	}
	switchBackDetails := switchBackConflictPayload["error"].(map[string]interface{})["details"].(map[string]interface{})
	if switchBackDetails["source_disposition"] != recoverySourcePreserve {
		t.Fatalf("historical formal source must remain preserved: %#v", switchBackDetails)
	}
	switchBackRecoveryID := switchBackDetails["recovery_id"].(string)
	status, switchBackPayload, _ := performJSON(t, h, http.MethodPost, "/api/v2/account-recoveries/"+switchBackRecoveryID+"/confirm", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusOK {
		t.Fatalf("switch-back status=%d payload=%#v", status, switchBackPayload)
	}
	switchBackData := responseData(t, switchBackPayload)
	if switchBackData["agent_id"] != strconv.FormatInt(sourceID, 10) || switchBackData["source_agent_abandoned"] != false {
		t.Fatalf("switch-back returned wrong identity: %#v", switchBackData)
	}

	var finalSourcePrincipals, finalTargetPrincipals, finalSourceBindings, finalTargetBindings int64
	if err := gdb.Raw(`SELECT COUNT(*) FROM agent_principals WHERE agent_id = ?`, sourceID).Scan(&finalSourcePrincipals).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw(`SELECT COUNT(*) FROM agent_principals WHERE agent_id = ? AND principal_id = ?`, targetID, principalID).Scan(&finalTargetPrincipals).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw(`SELECT COUNT(*) FROM agent_email_bindings WHERE agent_id = ? AND status = 'active'`, sourceID).Scan(&finalSourceBindings).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw(`SELECT COUNT(*) FROM agent_email_bindings WHERE agent_id = ? AND status = 'active'`, targetID).Scan(&finalTargetBindings).Error; err != nil {
		t.Fatal(err)
	}
	if finalSourcePrincipals != 1 || finalTargetPrincipals != 0 || finalSourceBindings != 1 || finalTargetBindings != 1 {
		t.Fatalf("final identity split or email loss: source_principals=%d target_principals=%d source_bindings=%d target_bindings=%d",
			finalSourcePrincipals, finalTargetPrincipals, finalSourceBindings, finalTargetBindings)
	}
}
