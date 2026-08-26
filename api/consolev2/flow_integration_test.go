package consolev2

import (
	"bytes"
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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/kitex/client/callopt"
	redis "github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"eigenflux_server/kitex_gen/eigenflux/base"
	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
	"eigenflux_server/pkg/config"
)

type capturedEmail struct {
	to  string
	otp string
}

type captureEmailSender struct {
	sent chan capturedEmail
}

type fakeFeedClient struct {
	authorID int64
	failNext atomic.Bool
}

type fakeNotificationClient struct{ acked atomic.Int32 }

func (f *fakeNotificationClient) ListPending(_ context.Context, _ *notificationrpc.ListPendingReq, _ ...callopt.Option) (*notificationrpc.ListPendingResp, error) {
	return &notificationrpc.ListPendingResp{
		Notifications: []*notificationrpc.PendingNotification{{
			NotificationId: 9001, SourceType: "system", Type: "system",
			Content: "Official platform maintenance notice", CreatedAt: time.Now().UnixMilli(),
		}},
		BaseResp: &base.BaseResp{Code: 0},
	}, nil
}

func (f *fakeNotificationClient) AckNotifications(_ context.Context, request *notificationrpc.AckNotificationsReq, _ ...callopt.Option) (*notificationrpc.AckNotificationsResp, error) {
	if len(request.Items) > 0 {
		f.acked.Add(int32(len(request.Items)))
	}
	return &notificationrpc.AckNotificationsResp{BaseResp: &base.BaseResp{Code: 0}}, nil
}

func (f *fakeFeedClient) ugcItemID() int64 { return f.authorID + 1001 }
func (f *fakeFeedClient) pgcItemID() int64 { return f.authorID + 1002 }

func (f *fakeFeedClient) FetchFeed(_ context.Context, _ *feedrpc.FetchFeedReq, _ ...callopt.Option) (*feedrpc.FetchFeedResp, error) {
	if f.failNext.Swap(false) {
		return nil, errors.New("transient Feed RPC failure")
	}
	summary := "Relevant infrastructure updates from an Agent-authored signal"
	ugcSource := "ugc"
	pgcSource := "pgc"
	pgcSummary := "A platform-curated signal"
	return &feedrpc.FetchFeedResp{
		Items: []*feedrpc.FeedItem{
			{ItemId: f.ugcItemID(), Summary: &summary, BroadcastType: "signal", SourceType: &ugcSource, UpdatedAt: time.Now().UnixMilli(), AuthorAgentId: &f.authorID},
			{ItemId: f.pgcItemID(), Summary: &pgcSummary, BroadcastType: "platform", SourceType: &pgcSource, UpdatedAt: time.Now().UnixMilli(), AuthorAgentId: &f.authorID},
		},
		HasMore: false, ImpressionId: "integration-impression", BaseResp: &base.BaseResp{Code: 0},
	}, nil
}

func (s *captureEmailSender) SendLoginVerifyMail(_ context.Context, to, otp string) error {
	s.sent <- capturedEmail{to: to, otp: otp}
	return nil
}

func performJSON(t *testing.T, h *server.Hertz, method, path string, body interface{}, headers ...ut.Header) (int, map[string]interface{}, [][]byte) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	headers = append(headers,
		ut.Header{Key: "Content-Type", Value: "application/json"},
		ut.Header{Key: "Origin", Value: "https://console.example.test"},
		ut.Header{Key: "Host", Value: "console.example.test"},
	)
	target := path
	if strings.HasPrefix(path, "/api/v2/") {
		target = "https://console.example.test" + path
	}
	recorder := ut.PerformRequest(h.Engine, method, target, &ut.Body{Body: bytes.NewReader(encoded), Len: len(encoded)}, headers...)
	resp := recorder.Result()
	var payload map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &payload); err != nil {
		t.Fatalf("decode response (%d): %v body=%s", resp.StatusCode(), err, resp.Body())
	}
	return resp.StatusCode(), payload, resp.Header.PeekAll("Set-Cookie")
}

func responseData(t *testing.T, payload map[string]interface{}) map[string]interface{} {
	t.Helper()
	data, ok := payload["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("response has no data object: %#v", payload)
	}
	return data
}

func responseErrorCode(t *testing.T, payload map[string]interface{}) string {
	t.Helper()
	apiError, ok := payload["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("response has no error object: %#v", payload)
	}
	code, _ := apiError["code"].(string)
	return code
}

func cookiePair(setCookies [][]byte, name string) string {
	prefix := name + "="
	for _, raw := range setCookies {
		text := string(raw)
		start := strings.Index(text, prefix)
		if start < 0 {
			continue
		}
		end := strings.IndexByte(text[start:], ';')
		if end < 0 {
			return text[start:]
		}
		return text[start : start+end]
	}
	return ""
}

func TestConsoleV2ProvisionHandoffAndOnboardingFlow(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for Console V2 PostgreSQL integration semantics")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	idgen := &fixedIDGenerator{id: time.Now().UnixMilli() * 1000}
	svc, err := NewService(gdb, idgen, &config.Config{
		ConsoleV2BootstrapSecret: "integration-broker-secret",
		ConsoleV2OTPPepper:       "integration-otp-pepper",
		ConsoleV2PublicURL:       "https://console.example.test",
		EnableFeedV2:             true,
		EnableControlChannelV2:   true,
		EnableAgentAttentionV1:   true,
		EnableCommunicationV2:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	redisServer := miniredis.RunT(t)
	svc.redisClient = redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = svc.redisClient.Close() })
	mailbox := make(chan capturedEmail, 4)
	svc.emailSender = &captureEmailSender{sent: mailbox}
	svc.startEmailWorkers(1, 16)
	fakeFeed := &fakeFeedClient{}
	svc.SetFeedClient(fakeFeed)
	fakeNotifications := &fakeNotificationClient{}
	svc.SetNotificationClient(fakeNotifications)
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	svc.Register(h)

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKeyEncoded := base64.RawURLEncoding.EncodeToString(publicKey)
	draft := map[string]interface{}{
		"identity_card": map[string]interface{}{"agent_name": "Integration Agent", "bio": "Tests the V2 flow"},
		"security_boundary": map[string]interface{}{
			"recurring_publish": false, "auto_reply_pm": false, "auto_comment": false, "show_add_friend": true,
		},
		"network_goal": "Find relevant infrastructure signals",
		"intent_actions": []map[string]interface{}{{
			"watch_for": "infrastructure updates", "trigger_when": "source is relevant",
			"action_instruction": "analyze and report", "action_policy": "analyze_only", "priority": 10,
		}},
	}
	issue := func(entitlement string) (string, string) {
		status, payload, _ := performJSON(t, h, "POST", "/api/v2/bootstrap-grants", map[string]interface{}{
			"entitlement_id": entitlement, "idempotency_key": "grant-" + hashString(entitlement),
			"channel": "integration", "policy": "limited", "public_key": publicKeyEncoded,
		}, ut.Header{Key: "X-Bootstrap-Broker-Secret", Value: "integration-broker-secret"})
		if status != 201 {
			t.Fatalf("issue grant status=%d payload=%#v", status, payload)
		}
		data := responseData(t, payload)
		return data["bootstrap_grant"].(string), data["nonce"].(string)
	}
	provision := func(entitlement string) map[string]interface{} {
		grant, nonce := issue(entitlement)
		draftJSON, _ := json.Marshal(draft)
		req := provisionRequest{
			BootstrapGrant: grant, IdempotencyKey: "provision-" + hashString(grant), Nonce: nonce, PublicKey: publicKeyEncoded,
			IssuedAt: time.Now().UnixMilli(), AgentName: "Integration Agent", Draft: draftJSON,
		}
		transcript, err := provisionTranscript(req)
		if err != nil {
			t.Fatal(err)
		}
		req.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, transcript))
		status, payload, _ := performJSON(t, h, "POST", "/api/v2/agent-identities/provision", req,
			ut.Header{Key: "X-Client-Host", Value: "workbuddy/5.3.14"},
			ut.Header{Key: "X-Client-Device-Name", Value: "Provision-MacBook"})
		if status != 200 {
			t.Fatalf("provision status=%d payload=%#v", status, payload)
		}
		return responseData(t, payload)
	}

	firstEntitlement := "integration-" + time.Now().Format("150405.000000000")
	first := provision(firstEntitlement)
	agentID := first["agent_id"].(string)
	agentIDInt := mustParseInt64(t, agentID)
	originalAccessToken := first["access_token"].(string)
	refreshToken := first["refresh_token"].(string)
	t.Cleanup(func() { gdb.Exec(`DELETE FROM agents WHERE agent_id = ?`, agentID) })
	if first["created"] != true {
		t.Fatal("first provision did not create the Agent")
	}
	provisionReplay := provision(firstEntitlement)
	if provisionReplay["agent_id"] != agentID || provisionReplay["access_token"] != originalAccessToken ||
		provisionReplay["refresh_token"] != refreshToken {
		t.Fatalf("identical provision retry did not replay the committed receipt: %#v", provisionReplay)
	}
	fakeFeed.authorID = agentIDInt
	if err := gdb.Exec(`INSERT INTO raw_items (item_id, author_agent_id, raw_content, raw_notes, raw_url, created_at)
		VALUES (?, ?, 'complete raw infrastructure source', '{}', 'https://example.test/source', ?),
		       (?, ?, 'complete curated source', '{}', '', ?)`,
		fakeFeed.ugcItemID(), agentIDInt, time.Now().UnixMilli(), fakeFeed.pgcItemID(), agentIDInt, time.Now().UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO processed_items
		(item_id, status, summary, broadcast_type, domains, keywords, source_type, expected_response, updated_at)
		VALUES (?, 3, 'infrastructure source', 'info', 'infra,agents', 'runtime', 'original', 'technical analysis', ?),
		       (?, 3, 'curated source', 'info', 'platform', 'news', 'curated', '', ?)`,
		fakeFeed.ugcItemID(), time.Now().UnixMilli(), fakeFeed.pgcItemID(), time.Now().UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		gdb.Exec(`DELETE FROM processed_items WHERE item_id IN (?, ?)`, fakeFeed.ugcItemID(), fakeFeed.pgcItemID())
		gdb.Exec(`DELETE FROM raw_items WHERE item_id IN (?, ?)`, fakeFeed.ugcItemID(), fakeFeed.pgcItemID())
	})
	fakeFeed.failNext.Store(true)
	retryRequest := pullFeedRequest{Limit: 20}
	status, _, _ := performJSON(t, h, "POST", "/api/v2/feed", retryRequest,
		ut.Header{Key: "Authorization", Value: "Bearer " + originalAccessToken})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("transient Feed failure status=%d", status)
	}
	status, retryPayload, _ := performJSON(t, h, "POST", "/api/v2/feed", retryRequest,
		ut.Header{Key: "Authorization", Value: "Bearer " + originalAccessToken})
	if status != http.StatusOK {
		t.Fatalf("same-key Feed retry did not recover status=%d payload=%#v", status, retryPayload)
	}
	status, baselinePayload, _ := performJSON(t, h, "POST", "/api/v2/feed", pullFeedRequest{Limit: 20},
		ut.Header{Key: "Authorization", Value: "Bearer " + originalAccessToken})
	if status != http.StatusOK {
		t.Fatalf("baseline Feed status=%d payload=%#v", status, baselinePayload)
	}
	baselineData := responseData(t, baselinePayload)
	baselinePersonalization := baselineData["personalization"].(map[string]interface{})
	if baselinePersonalization["mode"] != "baseline" || baselinePersonalization["context_revision"] != nil || baselineData["control_context_snapshot"] != nil {
		t.Fatalf("unfinished onboarding leaked context: %#v", baselineData)
	}
	for _, value := range baselineData["items"].([]interface{}) {
		item := value.(map[string]interface{})
		if item["intent_match"] != nil || len(item["recommended_actions"].([]interface{})) != 0 {
			t.Fatalf("baseline item leaked intent data: %#v", item)
		}
	}
	status, challengePayload, _ := performJSON(t, h, "POST", "/api/v2/agent-sessions/refresh-challenges", refreshChallengeRequest{
		RefreshToken: refreshToken, RotationRequestID: "refresh-" + hashString(refreshToken),
	})
	if status != 201 {
		t.Fatalf("refresh challenge status=%d payload=%#v", status, challengePayload)
	}
	challenge := responseData(t, challengePayload)
	refreshReq := refreshAgentSessionRequest{
		RefreshToken: refreshToken, RotationRequestID: "refresh-" + hashString(refreshToken),
		Nonce:     challenge["nonce"].(string),
		PublicKey: publicKeyEncoded,
		IssuedAt:  int64(challenge["issued_at"].(float64)),
	}
	refreshProof, err := refreshTranscript(refreshReq)
	if err != nil {
		t.Fatal(err)
	}
	refreshReq.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, refreshProof))
	status, refreshPayload, _ := performJSON(t, h, "POST", "/api/v2/agent-sessions/refresh", refreshReq)
	if status != 200 {
		t.Fatalf("refresh status=%d payload=%#v", status, refreshPayload)
	}
	refreshData := responseData(t, refreshPayload)
	accessToken := refreshData["access_token"].(string)
	rotatedRefreshToken := refreshData["refresh_token"].(string)
	status, replayChallengePayload, _ := performJSON(t, h, "POST", "/api/v2/agent-sessions/refresh-challenges", refreshChallengeRequest{
		RefreshToken: refreshToken, RotationRequestID: refreshReq.RotationRequestID,
	})
	if status != http.StatusCreated {
		t.Fatalf("refresh replay challenge status=%d payload=%#v", status, replayChallengePayload)
	}
	replayChallenge := responseData(t, replayChallengePayload)
	replayReq := refreshAgentSessionRequest{
		RefreshToken: refreshToken, RotationRequestID: refreshReq.RotationRequestID,
		Nonce: replayChallenge["nonce"].(string), PublicKey: publicKeyEncoded,
		IssuedAt: int64(replayChallenge["issued_at"].(float64)),
	}
	replayProof, _ := refreshTranscript(replayReq)
	replayReq.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, replayProof))
	status, refreshReplayPayload, _ := performJSON(t, h, "POST", "/api/v2/agent-sessions/refresh", replayReq)
	if status != http.StatusOK {
		t.Fatalf("refresh replay status=%d payload=%#v", status, refreshReplayPayload)
	}
	replayData := responseData(t, refreshReplayPayload)
	if replayData["access_token"] != accessToken || replayData["refresh_token"] != rotatedRefreshToken {
		t.Fatalf("refresh replay returned a different successor: %#v", replayData)
	}
	status, heartbeatPayload, _ := performJSON(t, h, "POST", "/api/v2/runtime/heartbeat", runtimeHeartbeatRequest{
		RuntimeInstanceID: "integration-runtime", Capabilities: []string{"commands"},
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != 200 {
		t.Fatalf("newly provisioned runtime heartbeat status=%d payload=%#v", status, heartbeatPayload)
	}
	status, _, _ = performJSON(t, h, "POST", "/api/v2/console/handoffs", map[string]interface{}{"browser_nonce": strings.Repeat("x", 32)},
		ut.Header{Key: "Authorization", Value: "Bearer " + originalAccessToken})
	if status != 401 {
		t.Fatalf("rotated access token remained valid, status=%d", status)
	}
	second := provision("integration-repeat-" + time.Now().Format("150405.000000000"))
	if second["agent_id"] != agentID || second["created"] != false {
		t.Fatalf("same public key did not reuse stable Agent: first=%#v second=%#v", first, second)
	}

	browserNonce := strings.Repeat("n", 32)
	status, handoffPayload, _ := performJSON(t, h, "POST", "/api/v2/console/handoffs", map[string]interface{}{"browser_nonce": browserNonce},
		ut.Header{Key: "Authorization", Value: "Bearer " + accessToken},
		ut.Header{Key: "X-Client-Host", Value: "codex"},
		ut.Header{Key: "X-Client-Device-Name", Value: "Lynn-MacBook-Pro"})
	if status != 201 {
		t.Fatalf("handoff status=%d payload=%#v", status, handoffPayload)
	}
	handoffURL, _ := url.Parse(responseData(t, handoffPayload)["handoff_url"].(string))
	ticket := handoffURL.Query().Get("ticket")
	status, exchangePayload, setCookies := performJSON(t, h, "POST", "/api/v2/console/handoffs/exchange", map[string]interface{}{"ticket": ticket, "browser_nonce": browserNonce})
	if status != 200 {
		t.Fatalf("exchange status=%d payload=%#v", status, exchangePayload)
	}
	exchangeData := responseData(t, exchangePayload)
	csrf := exchangeData["csrf_token"].(string)
	consoleCookie := cookiePair(setCookies, consoleCookieName)
	csrfCookie := cookiePair(setCookies, csrfCookieName)
	if consoleCookie == "" || csrfCookie == "" {
		t.Fatalf("exchange did not set both cookies: %q", setCookies)
	}
	cookieHeader := consoleCookie + "; " + csrfCookie

	status, unboundConfirmPayload, _ := performJSON(t, h, "POST", "/api/v2/agents/me/onboarding-draft/confirm", confirmStepRequest{
		Step: 2, ExpectedOnboardingRevision: 1, IdempotencyKey: "confirm-unbound-" + agentID,
	}, ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusConflict || responseErrorCode(t, unboundConfirmPayload) != "EMAIL_BINDING_REQUIRED" {
		t.Fatalf("unbound onboarding confirmation status=%d payload=%#v", status, unboundConfirmPayload)
	}

	boundEmail := fmt.Sprintf("console-v2-%s@example.com", agentID)
	status, bindChallengePayload, _ := performJSON(t, h, "POST", "/api/v2/account-email-bindings/challenges", createEmailChallengeRequest{
		Email: boundEmail,
	}, ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != 202 {
		t.Fatalf("email binding challenge status=%d payload=%#v", status, bindChallengePayload)
	}
	var bindMail capturedEmail
	select {
	case bindMail = <-mailbox:
	case <-time.After(2 * time.Second):
		t.Fatal("email binding OTP was not queued")
	}
	status, bindPayload, _ := performJSON(t, h, "POST", "/api/v2/account-email-bindings/verify", verifyEmailRequest{
		ChallengeID: responseData(t, bindChallengePayload)["challenge_id"].(string),
		Email:       boundEmail,
		OTP:         bindMail.otp,
	}, ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != 200 || responseData(t, bindPayload)["verification_level"] != "email_verified" {
		t.Fatalf("email binding verify status=%d payload=%#v", status, bindPayload)
	}

	revision := int64(1)
	for step := int16(2); step <= 5; step++ {
		status, payload, _ := performJSON(t, h, "POST", "/api/v2/agents/me/onboarding-draft/confirm", confirmStepRequest{
			Step: step, ExpectedOnboardingRevision: revision, IdempotencyKey: "confirm-" + agentID + fmt.Sprint(step),
		}, ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
		if status != 200 {
			t.Fatalf("confirm step %d status=%d payload=%#v", step, status, payload)
		}
		revision++
	}

	status, sessionPayload, _ := performJSON(t, h, "GET", "/api/v2/console/session", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != 200 {
		t.Fatalf("session status=%d payload=%#v", status, sessionPayload)
	}
	onboarding := responseData(t, sessionPayload)["onboarding"].(map[string]interface{})
	if onboarding["state"] != "completed" || onboarding["active_context_revision"] == nil {
		t.Fatalf("onboarding completion is not bound to an active context: %#v", onboarding)
	}
	session := responseData(t, sessionPayload)
	if session["runtime"] != "codex" || session["runtime_name"] != "codex" || session["runtime_version"] != "" {
		t.Fatalf("console session did not expose the latest handoff runtime: %#v", session)
	}
	if session["device_name"] != "Lynn-MacBook-Pro" {
		t.Fatalf("console session did not expose the handoff computer name: %#v", session)
	}
	var profileCompletedAt *int64
	if err := gdb.Raw(`SELECT profile_completed_at FROM agents WHERE agent_id = ?`, agentIDInt).
		Scan(&profileCompletedAt).Error; err != nil || profileCompletedAt == nil {
		t.Fatalf("V2 onboarding did not mark the profile complete: completed_at=%v err=%v", profileCompletedAt, err)
	}
	testCommunicationProjection(t, gdb, h, idgen, agentIDInt, cookieHeader)
	testTelemetryAggregation(t, gdb, h, agentIDInt, cookieHeader, csrf)
	testActivityCursorReset(t, gdb, h, idgen, agentIDInt, cookieHeader)
	testAgentAttentionProtocol(t, gdb, h, idgen, agentIDInt, accessToken, cookieHeader, csrf)

	status, boundSessionPayload, _ := performJSON(t, h, "GET", "/api/v2/console/session", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != 200 {
		t.Fatalf("bound session status=%d payload=%#v", status, boundSessionPayload)
	}
	boundSession := responseData(t, boundSessionPayload)
	if boundSession["email"] != boundEmail || boundSession["email_bound"] != true ||
		boundSession["verification_level"] != "email_verified" {
		t.Fatalf("bound session did not expose verified binding: %#v", boundSession)
	}
	var emailKind string
	if err := gdb.Raw(`SELECT email_kind FROM agents WHERE agent_id = ?`, agentID).Scan(&emailKind).Error; err != nil || emailKind != "v2_bound" {
		t.Fatalf("bound Agent email_kind=%q err=%v", emailKind, err)
	}
	status, recentAuthPayload, _ := performJSON(t, h, "POST", "/api/v2/agents/me/principals/challenges", createPrincipalChallengeRequest{
		PublicKey: publicKeyEncoded,
	}, ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusForbidden {
		t.Fatalf("handoff session unexpectedly passed recent-email-auth gate: status=%d payload=%#v", status, recentAuthPayload)
	}

	status, loginChallengePayload, _ := performJSON(t, h, "POST", "/api/v2/auth/email/challenges", createEmailChallengeRequest{
		Email: boundEmail, Purpose: "login",
	})
	if status != 202 {
		t.Fatalf("email login challenge status=%d payload=%#v", status, loginChallengePayload)
	}
	var loginMail capturedEmail
	select {
	case loginMail = <-mailbox:
	case <-time.After(2 * time.Second):
		t.Fatal("email login OTP was not queued")
	}
	status, loginPayload, loginCookies := performJSON(t, h, "POST", "/api/v2/auth/email/verify", verifyEmailRequest{
		ChallengeID: responseData(t, loginChallengePayload)["challenge_id"].(string),
		Email:       boundEmail,
		OTP:         loginMail.otp,
		Purpose:     "login",
	})
	if status != 200 || cookiePair(loginCookies, consoleCookieName) == "" {
		t.Fatalf("email login verify status=%d payload=%#v cookies=%q", status, loginPayload, loginCookies)
	}
	loginData := responseData(t, loginPayload)
	loginCookieHeader := cookiePair(loginCookies, consoleCookieName) + "; " + cookiePair(loginCookies, csrfCookieName)
	loginCSRF := loginData["csrf_token"].(string)
	devicePublicKey, devicePrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	devicePublicKeyEncoded := base64.RawURLEncoding.EncodeToString(devicePublicKey)
	status, deviceChallengePayload, _ := performJSON(t, h, "POST", "/api/v2/agents/me/principals/challenges", createPrincipalChallengeRequest{
		PublicKey: devicePublicKeyEncoded,
	}, ut.Header{Key: "Cookie", Value: loginCookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: loginCSRF})
	if status != http.StatusCreated {
		t.Fatalf("device challenge status=%d payload=%#v", status, deviceChallengePayload)
	}
	deviceChallenge := responseData(t, deviceChallengePayload)
	deviceReq := addPrincipalRequest{
		PublicKey: devicePublicKeyEncoded,
		Nonce:     deviceChallenge["nonce"].(string),
		IssuedAt:  int64(deviceChallenge["issued_at"].(float64)),
	}
	deviceTranscript, err := addPrincipalTranscript(deviceReq)
	if err != nil {
		t.Fatal(err)
	}
	deviceReq.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(devicePrivateKey, deviceTranscript))
	status, devicePayload, _ := performJSON(t, h, "POST", "/api/v2/agents/me/principals", deviceReq,
		ut.Header{Key: "Cookie", Value: loginCookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: loginCSRF})
	if status != http.StatusCreated || responseData(t, devicePayload)["access_token"] == "" {
		t.Fatalf("device link status=%d payload=%#v", status, devicePayload)
	}
	status, replayPayload, _ := performJSON(t, h, "POST", "/api/v2/agents/me/principals", deviceReq,
		ut.Header{Key: "Cookie", Value: loginCookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: loginCSRF})
	if status != http.StatusUnauthorized {
		t.Fatalf("add-device nonce replay was accepted: status=%d payload=%#v", status, replayPayload)
	}
	status, principalsPayload, _ := performJSON(t, h, "GET", "/api/v2/agents/me/principals", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: loginCookieHeader})
	if status != http.StatusOK || len(responseData(t, principalsPayload)["principals"].([]interface{})) < 2 {
		t.Fatalf("device list status=%d payload=%#v", status, principalsPayload)
	}
	status, contextPayload, _ := performJSON(t, h, "GET", "/api/v2/agents/me/control-context", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != 200 {
		t.Fatalf("control context status=%d payload=%#v", status, contextPayload)
	}

	// Bring the Agent to nine active intents, then race two writers against the
	// same context revision. The per-Agent head lock must admit exactly one,
	// keeping the hard product limit at ten without serializing other Agents.
	now := time.Now().UnixMilli()
	for i := 0; i < 8; i++ {
		if err := gdb.Exec(`INSERT INTO agent_intent_actions
			(agent_id, watch_for, trigger_when, action_instruction, action_policy, priority,
			 source, status, version, created_at, updated_at)
			VALUES (?, ?, 'relevant', 'analyze', 'analyze_only', 0, 'human_edit', 'active', 1, ?, ?)`,
			agentID, fmt.Sprintf("seed-%d", i), now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	contextRevision := int64(onboarding["active_context_revision"].(float64))
	fakeFeed.authorID = agentIDInt
	status, feedPayload, _ := performJSON(t, h, "POST", "/api/v2/feed", pullFeedRequest{Limit: 20},
		ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != 200 {
		t.Fatalf("feed batch status=%d payload=%#v", status, feedPayload)
	}
	feedData := responseData(t, feedPayload)
	feedItems := feedData["items"].([]interface{})
	if len(feedItems) != 2 || feedData["control_context_snapshot"] == nil || feedData["schema_version"] != "feed.v2" {
		t.Fatalf("Feed did not return items/context: %#v", feedData)
	}
	for _, removedField := range []string{"batch_id", "status", "lease"} {
		if _, exists := feedData[removedField]; exists {
			t.Fatalf("stateless Feed V2 unexpectedly returned %s: %#v", removedField, feedData)
		}
	}
	if contract, _ := feedData["output_contract"].(string); contract == "" {
		t.Fatalf("Feed V2 did not include the per-response safety contract: %#v", feedData)
	}
	ugc := feedItems[0].(map[string]interface{})
	pgc := feedItems[1].(map[string]interface{})
	if ugc["author_identity"] == nil || pgc["author_identity"] != nil || ugc["intent_match"] == nil {
		t.Fatalf("UGC/PGC identity policy mismatch: ugc=%#v pgc=%#v", ugc, pgc)
	}
	status, sourcePayload, _ := performJSON(t, h, "GET", fmt.Sprintf("/api/v2/feed/items/broadcast/%d", fakeFeed.ugcItemID()), map[string]interface{}{},
		ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != http.StatusOK || responseData(t, sourcePayload)["content"] != "complete raw infrastructure source" {
		t.Fatalf("typed Feed source detail status=%d payload=%#v", status, sourcePayload)
	}
	status, notificationPayload, _ := performJSON(t, h, "GET", "/api/v2/notifications/pending", map[string]interface{}{},
		ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != http.StatusOK {
		t.Fatalf("V2 notifications status=%d payload=%#v", status, notificationPayload)
	}
	notification := responseData(t, notificationPayload)["notifications"].([]interface{})[0].(map[string]interface{})
	issuer := notification["issuer_identity"].(map[string]interface{})
	if issuer["verification_level"] != "official" || notification["action_authority"] != "none" {
		t.Fatalf("platform notification identity/action boundary mismatch: %#v", notification)
	}
	status, notificationAckPayload, _ := performJSON(t, h, "POST", "/api/v2/notifications/ack", map[string]interface{}{
		"notifications": []map[string]interface{}{{"notification_id": 9001, "source_type": "system"}},
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != http.StatusOK || fakeNotifications.acked.Load() != 1 {
		t.Fatalf("V2 notification ack status=%d payload=%#v acked=%d", status, notificationAckPayload, fakeNotifications.acked.Load())
	}
	status, attentionPayload, _ := performJSON(t, h, "GET", "/api/v2/console/attention-items", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != http.StatusOK {
		t.Fatalf("attention list status=%d payload=%#v", status, attentionPayload)
	}
	attentionItems := responseData(t, attentionPayload)["attention_items"].([]interface{})
	if len(attentionItems) != 0 {
		t.Fatalf("the server must not author Attention items from Feed matches: %#v", attentionItems)
	}
	status, commandPayload, _ := performJSON(t, h, "POST", "/api/v2/agent-commands", createAgentCommandRequest{
		CommandType: "human_instruction", Payload: json.RawMessage(`{"instruction":"review the new signal"}`),
		IdempotencyKey: "command-" + agentID,
	}, ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != 201 {
		t.Fatalf("command create status=%d payload=%#v", status, commandPayload)
	}
	commandID := responseData(t, commandPayload)["command_id"].(string)
	duplicateErr := gdb.Exec(`INSERT INTO agent_commands
		(agent_id, attention_id, command_type, payload, payload_hash, required_context_revision,
		 status, idempotency_key, created_at)
		SELECT agent_id, attention_id, command_type, payload, payload_hash, required_context_revision,
		 status, idempotency_key, created_at FROM agent_commands WHERE command_id = ?`, commandID).Error
	if !isUniqueViolation(duplicateErr) {
		t.Fatalf("pgx unique violation was not recognized: %v", duplicateErr)
	}
	var outboxCount int64
	if err := gdb.Raw(`SELECT COUNT(*) FROM control_wakeup_outbox
		WHERE agent_id = ? AND event_type = 'command_available' AND entity_id = ?`, agentID, commandID).Scan(&outboxCount).Error; err != nil || outboxCount != 1 {
		t.Fatalf("command wakeup outbox count=%d err=%v", outboxCount, err)
	}
	claimedWakeups, err := svc.claimControlOutbox(time.Now().UnixMilli())
	claimedCommandWakeup := false
	for _, wakeup := range claimedWakeups {
		if wakeup.EntityID == mustParseInt64(t, commandID) {
			claimedCommandWakeup = true
			break
		}
	}
	if err != nil || !claimedCommandWakeup {
		t.Fatalf("command wakeup outbox claim mismatch: rows=%#v err=%v", claimedWakeups, err)
	}
	secondClaim, err := svc.claimControlOutbox(time.Now().UnixMilli())
	if err != nil || len(secondClaim) != 0 {
		t.Fatalf("leased wakeup was claimed twice: rows=%#v err=%v", secondClaim, err)
	}
	status, staleClaimPayload, _ := performJSON(t, h, "POST", "/api/v2/agent-commands/"+commandID+"/claim", claimAgentCommandRequest{
		RuntimeInstanceID: "integration-runtime", AppliedContextRevision: contextRevision,
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != http.StatusConflict || staleClaimPayload["error"].(map[string]interface{})["code"] != "CONTEXT_REQUIRED" {
		t.Fatalf("runtime self-reported context bypassed heartbeat authority: status=%d payload=%#v", status, staleClaimPayload)
	}
	status, heartbeatPayload, _ = performJSON(t, h, "POST", "/api/v2/runtime/heartbeat", runtimeHeartbeatRequest{
		RuntimeInstanceID: "integration-runtime", Capabilities: []string{"commands", "feed"},
		SessionRef: stringPointer("main-session"), AppliedContextRevision: &contextRevision,
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != http.StatusOK || len(responseData(t, heartbeatPayload)["pending_command_ids"].([]interface{})) != 1 {
		t.Fatalf("runtime heartbeat did not reconcile command: status=%d payload=%#v", status, heartbeatPayload)
	}
	status, claimPayload, _ := performJSON(t, h, "POST", "/api/v2/agent-commands/"+commandID+"/claim", claimAgentCommandRequest{
		RuntimeInstanceID: "integration-runtime", AppliedContextRevision: contextRevision,
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != 200 {
		t.Fatalf("command claim status=%d payload=%#v", status, claimPayload)
	}
	claimData := responseData(t, claimPayload)
	status, replayedClaimPayload, _ := performJSON(t, h, "POST", "/api/v2/agent-commands/"+commandID+"/claim", claimAgentCommandRequest{
		RuntimeInstanceID: "integration-runtime", AppliedContextRevision: contextRevision,
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != http.StatusOK {
		t.Fatalf("command claim response-loss replay status=%d payload=%#v", status, replayedClaimPayload)
	}
	replayedClaim := responseData(t, replayedClaimPayload)
	if replayedClaim["claim_token"] != claimData["claim_token"] || replayedClaim["claim_epoch"] != claimData["claim_epoch"] ||
		replayedClaim["attempt_count"] != claimData["attempt_count"] {
		t.Fatalf("command claim replay replaced fencing proof: first=%#v replay=%#v", claimData, replayedClaim)
	}
	status, completePayload, _ := performJSON(t, h, "POST", "/api/v2/agent-commands/"+commandID+"/complete", completeAgentCommandRequest{
		RuntimeInstanceID: "integration-runtime", ClaimEpoch: int64(claimData["claim_epoch"].(float64)),
		ClaimToken: claimData["claim_token"].(string), Status: "completed", Result: json.RawMessage(`{"handled":true}`),
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != 200 || responseData(t, completePayload)["status"] != "completed" {
		t.Fatalf("command complete status=%d payload=%#v", status, completePayload)
	}
	firstCompletedAt := responseData(t, completePayload)["completed_at"]
	status, completePayload, _ = performJSON(t, h, "POST", "/api/v2/agent-commands/"+commandID+"/complete", completeAgentCommandRequest{
		RuntimeInstanceID: "integration-runtime", ClaimEpoch: int64(claimData["claim_epoch"].(float64)),
		ClaimToken: claimData["claim_token"].(string), Status: "completed", Result: json.RawMessage(`{"handled": true}`),
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != 200 || responseData(t, completePayload)["completed_at"] != firstCompletedAt {
		t.Fatalf("semantic command completion retry was not idempotent: status=%d payload=%#v", status, completePayload)
	}

	var successes atomic.Int32
	var conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, _, mutationErr := svc.contextMutation(agentIDInt, "intent_concurrency_test",
				fmt.Sprintf("concurrent-%d", index), fmt.Sprintf("hash-%d", index), func(tx *gorm.DB, mutationNow int64) error {
					if err := lockContextHead(tx, agentIDInt, contextRevision); err != nil {
						return err
					}
					var count int64
					if err := tx.Raw(`SELECT COUNT(*) FROM agent_intent_actions WHERE agent_id = ? AND status = 'active'`, agentID).Scan(&count).Error; err != nil {
						return err
					}
					if count >= 10 {
						return errors.New("active intent limit reached")
					}
					return tx.Exec(`INSERT INTO agent_intent_actions
						(agent_id, watch_for, trigger_when, action_instruction, action_policy, priority,
						 source, status, version, created_at, updated_at)
						VALUES (?, ?, 'relevant', 'analyze', 'analyze_only', 0, 'human_edit', 'active', 1, ?, ?)`,
						agentID, fmt.Sprintf("concurrent-%d", index), mutationNow, mutationNow).Error
				})
			switch {
			case mutationErr == nil:
				successes.Add(1)
			case errors.Is(mutationErr, errConflict):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected concurrent mutation error: %v", mutationErr)
			}
		}(i)
	}
	wg.Wait()
	var activeCount int64
	if err := gdb.Raw(`SELECT COUNT(*) FROM agent_intent_actions WHERE agent_id = ? AND status = 'active'`, agentID).Scan(&activeCount).Error; err != nil {
		t.Fatal(err)
	}
	if successes.Load() != 1 || conflicts.Load() != 1 || activeCount != 10 {
		t.Fatalf("intent concurrency fence failed: success=%d conflict=%d active=%d", successes.Load(), conflicts.Load(), activeCount)
	}
	testLegacyEmailRecovery(t, gdb, h, idgen, mailbox)
}

func testLegacyEmailRecovery(t *testing.T, gdb *gorm.DB, h *server.Hertz, idgen *fixedIDGenerator, mailbox chan capturedEmail) {
	t.Helper()
	legacyAgentID, _ := idgen.NextID()
	now := time.Now().UnixMilli()
	email := fmt.Sprintf("legacy-v2-%d@example.com", legacyAgentID)
	if err := gdb.Exec(`INSERT INTO agents
		(agent_id, short_id, email, email_kind, agent_name, bio, created_at, updated_at)
		VALUES (?, 'LeGcy', ?, 'legacy_real', 'Legacy Recovery Agent', 'legacy bio', ?, ?)`, legacyAgentID, email, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_profiles (agent_id, status, updated_at) VALUES (?, 0, ?)`, legacyAgentID, now).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gdb.Exec(`DELETE FROM agents WHERE agent_id = ?`, legacyAgentID) })

	status, challengePayload, _ := performJSON(t, h, "POST", "/api/v2/auth/email/challenges", createEmailChallengeRequest{
		Email: email, Purpose: "recovery",
	})
	if status != http.StatusAccepted {
		t.Fatalf("legacy recovery challenge status=%d payload=%#v", status, challengePayload)
	}
	var mail capturedEmail
	select {
	case mail = <-mailbox:
	case <-time.After(2 * time.Second):
		t.Fatal("legacy recovery OTP was not queued")
	}
	status, verifyPayload, cookies := performJSON(t, h, "POST", "/api/v2/auth/email/verify", verifyEmailRequest{
		ChallengeID: responseData(t, challengePayload)["challenge_id"].(string),
		Email:       email, OTP: mail.otp, Purpose: "recovery",
	})
	if status != http.StatusOK || cookiePair(cookies, consoleCookieName) == "" {
		t.Fatalf("legacy recovery verify status=%d payload=%#v", status, verifyPayload)
	}
	var state, verificationState, emailKind string
	if err := gdb.Raw(`SELECT o.state, b.verification_state, a.email_kind
		FROM agents a JOIN agent_onboarding_v2 o ON o.agent_id = a.agent_id
		JOIN agent_email_bindings b ON b.agent_id = a.agent_id AND b.status = 'active'
		WHERE a.agent_id = ?`, legacyAgentID).Row().Scan(&state, &verificationState, &emailKind); err != nil {
		t.Fatal(err)
	}
	if state != "migration_pending" || verificationState != "verified" || emailKind != "legacy_real" {
		t.Fatalf("legacy recovery state mismatch: onboarding=%s binding=%s email_kind=%s", state, verificationState, emailKind)
	}
	var recoveryPrincipals int64
	if err := gdb.Raw(`SELECT COUNT(*) FROM agent_principals
		WHERE agent_id = ? AND key_type = 'email-recovery-v1'`, legacyAgentID).Scan(&recoveryPrincipals).Error; err != nil || recoveryPrincipals != 1 {
		t.Fatalf("legacy recovery principal count=%d err=%v", recoveryPrincipals, err)
	}
}

func testTelemetryAggregation(t *testing.T, gdb *gorm.DB, h *server.Hertz, agentID int64, cookieHeader, csrf string) {
	t.Helper()
	now := time.Now().UnixMilli()
	bucket := now - now%telemetryBucketMS
	eventID := fmt.Sprintf("telemetry-%d", agentID)
	usageSessionID := fmt.Sprintf("usage-session-%d", agentID)
	request := telemetryBatchRequest{
		Events: []telemetryEventRequest{{
			EventID: eventID, EventType: "dashboard_first_render", EventAt: now,
			Properties: map[string]interface{}{"route": "/dashboard/today"},
		}},
		Usage: &telemetryUsageRequest{
			SessionID: usageSessionID, TimeBucket: bucket, VisibleDurationMS: 60000,
			FirstEventAt: now, LastEventAt: now,
		},
	}
	status, payload, _ := performJSON(t, h, "POST", "/api/v2/telemetry/events:batch", request,
		ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusAccepted || responseData(t, payload)["accepted_events"] != float64(1) {
		t.Fatalf("telemetry batch status=%d payload=%#v", status, payload)
	}
	request.Usage.VisibleDurationMS = 30000
	status, payload, _ = performJSON(t, h, "POST", "/api/v2/telemetry/events:batch", request,
		ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusAccepted || responseData(t, payload)["accepted_events"] != float64(0) {
		t.Fatalf("telemetry replay was not idempotent: status=%d payload=%#v", status, payload)
	}
	var duration int64
	if err := gdb.Raw(`SELECT visible_duration_ms FROM console_usage_sessions
		WHERE session_id = ? AND time_bucket = ?`, usageSessionID, bucket).Scan(&duration).Error; err != nil || duration != 60000 {
		t.Fatalf("usage aggregation regressed on replay: duration=%d err=%v", duration, err)
	}
	t.Cleanup(func() {
		gdb.Exec(`DELETE FROM telemetry_events_v2 WHERE event_id = ?`, eventID)
		gdb.Exec(`DELETE FROM console_usage_sessions WHERE session_id = ?`, usageSessionID)
	})
}

func testAgentAttentionProtocol(t *testing.T, gdb *gorm.DB, h *server.Hertz, idgen *fixedIDGenerator,
	agentID int64, accessToken, cookieHeader, csrf string,
) {
	t.Helper()
	now := time.Now().UnixMilli()
	activityID, _ := idgen.NextID()
	if err := gdb.Exec(`INSERT INTO agent_activity_log
		(log_id, agent_id, event_type, summary, detail, created_at, source_event_id)
		VALUES (?, ?, 'profile_update', 'Agent Card updated', '{}'::jsonb, ?, ?)`,
		activityID, agentID, now, fmt.Sprintf("attention-source-%d", activityID)).Error; err != nil {
		t.Fatal(err)
	}
	request := attentionPublishRequest{
		SchemaVersion:  "agent_attention.v1",
		IdempotencyKey: fmt.Sprintf("attention-upload-%d", activityID),
		Items: []attentionPublishItem{
			{
				ClientItemID: fmt.Sprintf("decision-%d", activityID), Surface: "participation",
				Category: "other_decision", Language: "en", Title: "Choose the next research direction",
				Body: "The Agent found two valid research paths.", Recommendation: "Continue with the higher-confidence path.",
				Actions: []attentionProtocolAction{
					{ActionKey: "a1", Kind: "custom", Flag: "Continue", Appearance: "primary"},
					{ActionKey: "a2", Kind: "preset", Flag: "observe_first", Appearance: "secondary"},
				},
				GeneratedAt: now, ExpiresAt: now + int64(24*time.Hour/time.Millisecond),
			},
			{
				ClientItemID: fmt.Sprintf("signal-%d", activityID), Surface: "focus",
				Category: "other_attention", Language: "en", Title: "Agent Card was updated",
				Body:      "The latest Agent Card projection is ready to review.",
				SourceRef: &attentionSourceRef{Type: "activity", ID: strconv.FormatInt(activityID, 10)},
				Actions: []attentionProtocolAction{
					{ActionKey: "source", Kind: "preset", Flag: "open_source", Appearance: "primary"},
					{ActionKey: "dismiss", Kind: "preset", Flag: "not_interested", Appearance: "secondary"},
				},
				GeneratedAt: now, ExpiresAt: now + int64(24*time.Hour/time.Millisecond),
			},
		},
	}
	status, publishPayload, _ := performJSON(t, h, "POST", "/api/v2/agent-attention-items:publish", request,
		ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != http.StatusCreated {
		t.Fatalf("Attention publish status=%d payload=%#v", status, publishPayload)
	}
	publishData := responseData(t, publishPayload)
	if publishData["accepted"] != float64(2) || publishData["replay"] != false {
		t.Fatalf("Attention publish result mismatch: %#v", publishData)
	}
	items := publishData["items"].([]interface{})
	attentionIDs := make(map[string]string, len(items))
	for _, raw := range items {
		item := raw.(map[string]interface{})
		attentionIDs[item["client_item_id"].(string)] = item["attention_id"].(string)
	}
	decisionID := attentionIDs[fmt.Sprintf("decision-%d", activityID)]
	signalID := attentionIDs[fmt.Sprintf("signal-%d", activityID)]
	if decisionID == "" || signalID == "" {
		t.Fatalf("Attention IDs missing from publish response: %#v", publishData)
	}
	status, legacyPayload, _ := performJSON(t, h, "POST", "/api/v2/agent-commands", createAgentCommandRequest{
		CommandType: "attention_action", Payload: json.RawMessage(`{}`), AttentionID: &decisionID,
		ActionIdempotencyKey: "legacy-action", IdempotencyKey: fmt.Sprintf("legacy-attention-%d", activityID),
	}, ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusGone || responseErrorCode(t, legacyPayload) != "LEGACY_ATTENTION_UNSUPPORTED" {
		t.Fatalf("legacy Attention command was not explicitly rejected: status=%d payload=%#v", status, legacyPayload)
	}

	status, replayPayload, _ := performJSON(t, h, "POST", "/api/v2/agent-attention-items:publish", request,
		ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != http.StatusOK || responseData(t, replayPayload)["replay"] != true {
		t.Fatalf("Attention publish replay status=%d payload=%#v", status, replayPayload)
	}

	status, sourcePayload, _ := performJSON(t, h, "GET", "/api/v2/console/attention-items/"+signalID+"/source", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != http.StatusOK || responseData(t, sourcePayload)["detail"].(map[string]interface{})["log_id"] != strconv.FormatInt(activityID, 10) {
		t.Fatalf("Attention source status=%d payload=%#v", status, sourcePayload)
	}

	response := respondAttentionRequest{
		ActionKey: "a1", ExpectedItemRevision: 1,
		IdempotencyKey: fmt.Sprintf("attention-response-%d", activityID),
	}
	status, responsePayload, _ := performJSON(t, h, "POST", "/api/v2/console/attention-items/"+decisionID+"/respond", response,
		ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusAccepted {
		t.Fatalf("Attention response status=%d payload=%#v", status, responsePayload)
	}
	responseDataValue := responseData(t, responsePayload)
	if responseDataValue["selected_flag"] != "Continue" || responseDataValue["command_status"] != "pending" {
		t.Fatalf("Attention response did not freeze the selected action: %#v", responseDataValue)
	}
	commandID := responseDataValue["command_id"].(string)
	status, responseReplayPayload, _ := performJSON(t, h, "POST", "/api/v2/console/attention-items/"+decisionID+"/respond", response,
		ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != http.StatusOK || responseData(t, responseReplayPayload)["selected_flag"] != "Continue" ||
		responseData(t, responseReplayPayload)["replay"] != true {
		t.Fatalf("Attention response replay status=%d payload=%#v", status, responseReplayPayload)
	}

	var commandCount int64
	if err := gdb.Raw(`SELECT COUNT(*) FROM agent_commands
		WHERE agent_id = ? AND attention_id = ? AND command_type = 'attention_response'`,
		agentID, mustParseInt64(t, decisionID)).Scan(&commandCount).Error; err != nil || commandCount != 1 {
		t.Fatalf("Attention response command count=%d err=%v", commandCount, err)
	}
	var frozenPayload string
	if err := gdb.Raw(`SELECT payload::text FROM agent_commands
		WHERE agent_id = ? AND attention_id = ? AND command_type = 'attention_response'`,
		agentID, mustParseInt64(t, decisionID)).Scan(&frozenPayload).Error; err != nil {
		t.Fatalf("Attention response payload read failed: %v", err)
	}
	var commandPayload map[string]interface{}
	if err := json.Unmarshal([]byte(frozenPayload), &commandPayload); err != nil {
		t.Fatalf("Attention response payload decode failed: %v", err)
	}
	snapshot, ok := commandPayload["attention_snapshot"].(map[string]interface{})
	if !ok {
		t.Fatalf("Attention response omitted frozen snapshot: %#v", commandPayload)
	}
	for _, key := range []string{"title", "body", "recommendation", "source_ref", "category", "surface", "language", "actions", "context_ref"} {
		if _, exists := snapshot[key]; !exists {
			t.Fatalf("Attention response snapshot omitted %q: %#v", key, snapshot)
		}
	}
	var contextRevision int64
	if err := gdb.Raw(`SELECT active_revision FROM agent_context_heads WHERE agent_id = ?`, agentID).Scan(&contextRevision).Error; err != nil || contextRevision <= 0 {
		t.Fatalf("Attention response context revision=%d err=%v", contextRevision, err)
	}
	status, heartbeatPayload, _ := performJSON(t, h, "POST", "/api/v2/runtime/heartbeat", runtimeHeartbeatRequest{
		RuntimeInstanceID: "attention-runtime", Capabilities: []string{"commands"},
		AppliedContextRevision: &contextRevision,
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != http.StatusOK {
		t.Fatalf("Attention runtime heartbeat status=%d payload=%#v", status, heartbeatPayload)
	}
	status, claimPayload, _ := performJSON(t, h, "POST", "/api/v2/agent-commands/"+commandID+"/claim", claimAgentCommandRequest{
		RuntimeInstanceID: "attention-runtime", AppliedContextRevision: contextRevision,
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != http.StatusOK {
		t.Fatalf("Attention command claim status=%d payload=%#v", status, claimPayload)
	}
	claimData := responseData(t, claimPayload)
	status, invalidCompletePayload, _ := performJSON(t, h, "POST", "/api/v2/agent-commands/"+commandID+"/complete", completeAgentCommandRequest{
		RuntimeInstanceID: "attention-runtime", ClaimEpoch: int64(claimData["claim_epoch"].(float64)),
		ClaimToken: claimData["claim_token"].(string), Status: "completed",
		Result: json.RawMessage(`{"summary":"Applied.","related_entities":[{"type":"broadcast","id":"123","url":"http://127.0.0.1/private"}]}`),
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != http.StatusBadRequest || responseErrorCode(t, invalidCompletePayload) != "INVALID_ATTENTION_RESULT" {
		t.Fatalf("unsafe Attention command result status=%d payload=%#v", status, invalidCompletePayload)
	}
	status, invalidCompletePayload, _ = performJSON(t, h, "POST", "/api/v2/agent-commands/"+commandID+"/complete", completeAgentCommandRequest{
		RuntimeInstanceID: "attention-runtime", ClaimEpoch: int64(claimData["claim_epoch"].(float64)),
		ClaimToken: claimData["claim_token"].(string), Status: "completed",
		Result: json.RawMessage(`{"summary":"Applied.","related_entities":[{"type":"broadcast","id":"999999999"}]}`),
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != http.StatusBadRequest || responseErrorCode(t, invalidCompletePayload) != "INVALID_ATTENTION_RESULT" {
		t.Fatalf("unauthorized Attention related entity status=%d payload=%#v", status, invalidCompletePayload)
	}
	status, completePayload, _ := performJSON(t, h, "POST", "/api/v2/agent-commands/"+commandID+"/complete", completeAgentCommandRequest{
		RuntimeInstanceID: "attention-runtime", ClaimEpoch: int64(claimData["claim_epoch"].(float64)),
		ClaimToken: claimData["claim_token"].(string), Status: "completed",
		Result: json.RawMessage(`{"summary":"Applied the selected Attention action."}`),
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != http.StatusOK || responseData(t, completePayload)["status"] != "completed" {
		t.Fatalf("Attention command complete status=%d payload=%#v", status, completePayload)
	}
	status, todayPayload, _ := performJSON(t, h, "GET", "/api/v2/console/today", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != http.StatusOK {
		t.Fatalf("Today Attention status=%d payload=%#v", status, todayPayload)
	}
	today := responseData(t, todayPayload)
	if len(today["participation_items"].([]interface{})) == 0 || len(today["focus_items"].([]interface{})) == 0 {
		t.Fatalf("Today did not expose both Attention surfaces: %#v", today)
	}
	brief := today["brief"].(map[string]interface{})
	if brief["participation_count"].(float64) < 1 || brief["focus_count"].(float64) < 1 {
		t.Fatalf("Today counts excluded visible acted Attention: %#v", brief)
	}
	participation := today["participation_items"].([]interface{})[0].(map[string]interface{})
	latest := participation["latest_command"].(map[string]interface{})
	if participation["status"] != "acted" || latest["status"] != "completed" {
		t.Fatalf("Today did not retain the completed Attention receipt: %#v", participation)
	}
	if err := gdb.Exec(`UPDATE agent_attention_items SET expires_at = ?
		WHERE agent_id = ? AND attention_id = ?`, time.Now().Add(-time.Second).UnixMilli(), agentID, mustParseInt64(t, signalID)).Error; err != nil {
		t.Fatal(err)
	}
	status, expiredPayload, _ := performJSON(t, h, "GET", "/api/v2/console/attention-items/"+signalID+"/source", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != http.StatusNotFound || responseErrorCode(t, expiredPayload) != "ATTENTION_SOURCE_NOT_FOUND" {
		t.Fatalf("expired Attention source remained visible: status=%d payload=%#v", status, expiredPayload)
	}

	t.Cleanup(func() {
		gdb.Exec(`DELETE FROM control_wakeup_outbox WHERE agent_id = ? AND entity_id IN
			(SELECT command_id FROM agent_commands WHERE agent_id = ? AND attention_id IN (?, ?))`,
			agentID, agentID, mustParseInt64(t, decisionID), mustParseInt64(t, signalID))
		gdb.Exec(`DELETE FROM agent_commands WHERE agent_id = ? AND attention_id IN (?, ?)`,
			agentID, mustParseInt64(t, decisionID), mustParseInt64(t, signalID))
		gdb.Exec(`DELETE FROM agent_attention_items WHERE agent_id = ? AND attention_id IN (?, ?)`,
			agentID, mustParseInt64(t, decisionID), mustParseInt64(t, signalID))
		gdb.Exec(`DELETE FROM agent_activity_log WHERE log_id = ?`, activityID)
	})
}

func testActivityCursorReset(t *testing.T, gdb *gorm.DB, h *server.Hertz, idgen *fixedIDGenerator, agentID int64, cookieHeader string) {
	t.Helper()
	now := time.Now().UnixMilli()
	firstID, _ := idgen.NextID()
	secondID, _ := idgen.NextID()
	if err := gdb.Exec(`INSERT INTO agent_activity_log
		(log_id, agent_id, event_type, summary, detail, created_at, agent_seq, source_event_id)
		VALUES (?, ?, 'feed_pull', 'first retained activity', '{}'::jsonb, ?, 5, ?),
		       (?, ?, 'broadcast', 'second retained activity', '{}'::jsonb, ?, 6, ?)`,
		firstID, agentID, now, fmt.Sprintf("integration-activity-%d", firstID),
		secondID, agentID, now+1, fmt.Sprintf("integration-activity-%d", secondID)).Error; err != nil {
		t.Fatal(err)
	}
	status, payload, _ := performJSON(t, h, "GET", "/api/v2/console/activity?after=1", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != http.StatusOK {
		t.Fatalf("activity list status=%d payload=%#v", status, payload)
	}
	data := responseData(t, payload)
	if data["cursor_reset"] != true || data["oldest_available_cursor"] != float64(4) || len(data["events"].([]interface{})) != 2 {
		t.Fatalf("activity retention cursor was not reset safely: %#v", data)
	}
}

func testCommunicationProjection(t *testing.T, gdb *gorm.DB, h *server.Hertz, idgen *fixedIDGenerator, viewerID int64, cookieHeader string) {
	t.Helper()
	now := time.Now().UnixMilli()
	peerID, _ := idgen.NextID()
	requestPeerID, _ := idgen.NextID()
	convID, _ := idgen.NextID()
	msgID, _ := idgen.NextID()
	unbrokenConvID, _ := idgen.NextID()
	unbrokenMsgID, _ := idgen.NextID()
	requestID, _ := idgen.NextID()
	foreignConvID, _ := idgen.NextID()
	publicCard := `{"agent_description":"Public Agent description","human_description":"Public human description","working_languages":["zh","en"],"seeking":["signals"],"offering":["analysis"]}`
	privateCard := `{"current_focus":["PRIVATE_FOCUS_MUST_NOT_LEAK"],"human_status":["PRIVATE_STATUS_MUST_NOT_LEAK"]}`
	if err := gdb.Exec(`INSERT INTO agents (agent_id, short_id, email, agent_name, bio, created_at, updated_at, is_official)
		VALUES (?, 'OfPee', ?, 'Official Peer', 'peer bio', ?, ?, true),
		       (?, 'RqPee', ?, 'Request Peer', 'request bio', ?, ?, false)`,
		peerID, fmt.Sprintf("peer-%d@example.com", peerID), now, now,
		requestPeerID, fmt.Sprintf("request-peer-%d@example.com", requestPeerID), now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO agent_cards
		(agent_id, public_card, private_card, schema_version, source_version, rebuild_fence,
		 card_version, public_card_version, generated_at, public_card_generated_at)
		VALUES (?, ?::jsonb, ?::jsonb, 1, 1, 1, 1, 7, ?, ?)`, peerID, publicCard, privateCard, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO user_relations (from_uid, to_uid, rel_type, remark, created_at)
		VALUES (?, ?, 1, 'viewer-only remark', ?), (?, ?, 1, '', ?)`, viewerID, peerID, now, peerID, viewerID, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO conversations
		(conv_id, participant_a, participant_b, initiator_id, last_sender_id, origin_type, msg_count, status, updated_at)
		VALUES (?, ?, ?, ?, ?, 'friend', 1, 0, ?),
		       (?, ?, ?, ?, ?, 'broadcast', 1, 0, ?),
		       (?, ?, ?, ?, ?, 'broadcast', 0, 0, ?)`,
		convID, viewerID, peerID, viewerID, peerID, now,
		unbrokenConvID, viewerID, requestPeerID, requestPeerID, requestPeerID, now,
		foreignConvID, peerID, requestPeerID, peerID, requestPeerID, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO private_messages
		(msg_id, conv_id, sender_id, receiver_id, content, is_read, created_at)
		VALUES (?, ?, ?, ?, 'hello from peer', false, ?),
		       (?, ?, ?, ?, 'cold inbound message', false, ?)`,
		msgID, convID, peerID, viewerID, now,
		unbrokenMsgID, unbrokenConvID, requestPeerID, viewerID, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO friend_requests
		(id, from_uid, to_uid, status, greeting, remark, created_at, updated_at)
		VALUES (?, ?, ?, 0, 'hello', '', ?, ?)`, requestID, viewerID, requestPeerID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		gdb.Exec(`DELETE FROM private_messages WHERE conv_id IN (?, ?, ?)`, convID, unbrokenConvID, foreignConvID)
		gdb.Exec(`DELETE FROM conversations WHERE conv_id IN (?, ?, ?)`, convID, unbrokenConvID, foreignConvID)
		gdb.Exec(`DELETE FROM friend_requests WHERE id = ?`, requestID)
		gdb.Exec(`DELETE FROM user_relations WHERE from_uid IN (?, ?) OR to_uid IN (?, ?)`, peerID, requestPeerID, peerID, requestPeerID)
		gdb.Exec(`DELETE FROM agents WHERE agent_id IN (?, ?)`, peerID, requestPeerID)
	})

	status, friendsPayload, _ := performJSON(t, h, "GET", "/api/v2/console/relations/friends", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != 200 {
		t.Fatalf("friends status=%d payload=%#v", status, friendsPayload)
	}
	friendsData := responseData(t, friendsPayload)
	contexts := friendsData["agent_contexts"].(map[string]interface{})
	peerContext := contexts[strconv.FormatInt(peerID, 10)].(map[string]interface{})
	identity := peerContext["identity_assertion"].(map[string]interface{})
	if identity["verification_level"] != "official" || peerContext["public_card_version"] != float64(7) {
		t.Fatalf("fresh identity/public Card version mismatch: %#v", peerContext)
	}
	encoded, _ := json.Marshal(friendsPayload)
	if strings.Contains(string(encoded), "PRIVATE_FOCUS_MUST_NOT_LEAK") || strings.Contains(string(encoded), "PRIVATE_STATUS_MUST_NOT_LEAK") {
		t.Fatalf("private Agent Card fields leaked: %s", encoded)
	}
	status, conversationsPayload, _ := performJSON(t, h, "GET", "/api/v2/console/pm/conversations", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != 200 {
		t.Fatalf("conversations status=%d payload=%#v", status, conversationsPayload)
	}
	conversations := responseData(t, conversationsPayload)["conversations"].([]interface{})
	if len(conversations) != 2 || conversations[0].(map[string]interface{})["last_message"] == nil {
		t.Fatalf("conversation batch enrichment mismatch: %#v", conversations)
	}
	status, unbrokenPayload, _ := performJSON(t, h, "GET", "/api/v2/console/pm/conversations?origin_type=unbroken", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != 200 {
		t.Fatalf("unbroken conversations status=%d payload=%#v", status, unbrokenPayload)
	}
	unbroken := responseData(t, unbrokenPayload)["conversations"].([]interface{})
	if len(unbroken) != 1 || unbroken[0].(map[string]interface{})["msg_count"] != float64(1) || unbroken[0].(map[string]interface{})["category"] != "non_friend" {
		t.Fatalf("unbroken conversation compatibility mismatch: %#v", unbroken)
	}

	status, outgoingPayload, _ := performJSON(t, h, "GET", "/api/v2/console/relations/friend-requests?direction=outgoing", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != 200 {
		t.Fatalf("outgoing requests status=%d payload=%#v", status, outgoingPayload)
	}
	requests := responseData(t, outgoingPayload)["friend_requests"].([]interface{})
	if len(requests) != 1 || requests[0].(map[string]interface{})["peer_agent_id"] != strconv.FormatInt(requestPeerID, 10) {
		t.Fatalf("outgoing request did not reference its recipient: %#v", requests)
	}

	status, unauthorizedPayload, _ := performJSON(t, h, "GET", fmt.Sprintf("/api/v2/console/pm/conversations/%d/messages", foreignConvID), map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != 404 {
		t.Fatalf("non-member conversation was not hidden: status=%d payload=%#v", status, unauthorizedPayload)
	}

	if err := gdb.Exec(`INSERT INTO user_relations (from_uid, to_uid, rel_type, remark, created_at)
		VALUES (?, ?, 2, '', ?)`, peerID, viewerID, now).Error; err != nil {
		t.Fatal(err)
	}
	status, messagesPayload, _ := performJSON(t, h, "GET", fmt.Sprintf("/api/v2/console/pm/conversations/%d/messages", convID), map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: cookieHeader})
	if status != 200 {
		t.Fatalf("messages status=%d payload=%#v", status, messagesPayload)
	}
	blockedContext := responseData(t, messagesPayload)["agent_contexts"].(map[string]interface{})[strconv.FormatInt(peerID, 10)].(map[string]interface{})
	if blockedContext["profile_status"] != "unavailable" || blockedContext["public_card_version"] != float64(0) {
		t.Fatalf("blocked peer retained public enrichment: %#v", blockedContext)
	}
}

func mustParseInt64(t *testing.T, value string) int64 {
	t.Helper()
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func stringPointer(value string) *string { return &value }
