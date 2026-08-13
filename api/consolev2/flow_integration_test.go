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

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/kitex/client/callopt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"eigenflux_server/kitex_gen/eigenflux/base"
	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
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
}

func (f *fakeFeedClient) FetchFeed(_ context.Context, _ *feedrpc.FetchFeedReq, _ ...callopt.Option) (*feedrpc.FetchFeedResp, error) {
	summary := "A relevant Agent-authored signal"
	ugcSource := "ugc"
	pgcSource := "pgc"
	pgcSummary := "A platform-curated signal"
	return &feedrpc.FetchFeedResp{
		Items: []*feedrpc.FeedItem{
			{ItemId: 1001, Summary: &summary, BroadcastType: "signal", SourceType: &ugcSource, UpdatedAt: time.Now().UnixMilli(), AuthorAgentId: &f.authorID},
			{ItemId: 1002, Summary: &pgcSummary, BroadcastType: "platform", SourceType: &pgcSource, UpdatedAt: time.Now().UnixMilli(), AuthorAgentId: &f.authorID},
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
	headers = append(headers, ut.Header{Key: "Content-Type", Value: "application/json"})
	recorder := ut.PerformRequest(h.Engine, method, path, &ut.Body{Body: bytes.NewReader(encoded), Len: len(encoded)}, headers...)
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
		EnableCommunicationV2:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox := make(chan capturedEmail, 4)
	svc.emailSender = &captureEmailSender{sent: mailbox}
	svc.startEmailWorkers(1, 16)
	fakeFeed := &fakeFeedClient{}
	svc.SetFeedClient(fakeFeed)
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
			"entitlement_id": entitlement, "channel": "integration", "policy": "limited", "public_key": publicKeyEncoded,
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
			BootstrapGrant: grant, Nonce: nonce, PublicKey: publicKeyEncoded,
			IssuedAt: time.Now().UnixMilli(), AgentName: "Integration Agent", Draft: draftJSON,
		}
		transcript, err := provisionTranscript(req)
		if err != nil {
			t.Fatal(err)
		}
		req.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, transcript))
		status, payload, _ := performJSON(t, h, "POST", "/api/v2/agent-identities/provision", req)
		if status != 200 {
			t.Fatalf("provision status=%d payload=%#v", status, payload)
		}
		return responseData(t, payload)
	}

	first := provision("integration-" + time.Now().Format("150405.000000000"))
	agentID := first["agent_id"].(string)
	originalAccessToken := first["access_token"].(string)
	refreshToken := first["refresh_token"].(string)
	t.Cleanup(func() { gdb.Exec(`DELETE FROM agents WHERE agent_id = ?`, agentID) })
	if first["created"] != true {
		t.Fatal("first provision did not create the Agent")
	}
	status, challengePayload, _ := performJSON(t, h, "POST", "/api/v2/agent-sessions/refresh-challenges", refreshChallengeRequest{
		RefreshToken: refreshToken,
	})
	if status != 201 {
		t.Fatalf("refresh challenge status=%d payload=%#v", status, challengePayload)
	}
	challenge := responseData(t, challengePayload)
	refreshReq := refreshAgentSessionRequest{
		RefreshToken: refreshToken,
		Nonce:        challenge["nonce"].(string),
		PublicKey:    publicKeyEncoded,
		IssuedAt:     int64(challenge["issued_at"].(float64)),
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
	accessToken := responseData(t, refreshPayload)["access_token"].(string)
	status, _, _ = performJSON(t, h, "POST", "/api/v2/console/handoffs", map[string]interface{}{},
		ut.Header{Key: "Authorization", Value: "Bearer " + originalAccessToken})
	if status != 401 {
		t.Fatalf("rotated access token remained valid, status=%d", status)
	}
	second := provision("integration-repeat-" + time.Now().Format("150405.000000000"))
	if second["agent_id"] != agentID || second["created"] != false {
		t.Fatalf("same public key did not reuse stable Agent: first=%#v second=%#v", first, second)
	}

	status, handoffPayload, _ := performJSON(t, h, "POST", "/api/v2/console/handoffs", map[string]interface{}{},
		ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != 201 {
		t.Fatalf("handoff status=%d payload=%#v", status, handoffPayload)
	}
	handoffURL, _ := url.Parse(responseData(t, handoffPayload)["handoff_url"].(string))
	ticket := handoffURL.Query().Get("ticket")
	status, exchangePayload, setCookies := performJSON(t, h, "POST", "/api/v2/console/handoffs/exchange", map[string]interface{}{"ticket": ticket})
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
	agentIDInt := mustParseInt64(t, agentID)
	testCommunicationProjection(t, gdb, h, idgen, agentIDInt, cookieHeader)
	testTelemetryAggregation(t, gdb, h, agentIDInt, cookieHeader, csrf)

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
	var emailKind string
	if err := gdb.Raw(`SELECT email_kind FROM agents WHERE agent_id = ?`, agentID).Scan(&emailKind).Error; err != nil || emailKind != "v2_bound" {
		t.Fatalf("bound Agent email_kind=%q err=%v", emailKind, err)
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
	status, feedPayload, _ := performJSON(t, h, "POST", "/api/v2/feed/batches", createFeedBatchRequest{
		ProcessingScope: "heartbeat", RuntimeInstanceID: "integration-runtime",
		IdempotencyKey: "feed-" + agentID, Limit: 20,
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != 200 {
		t.Fatalf("feed batch status=%d payload=%#v", status, feedPayload)
	}
	feedData := responseData(t, feedPayload)
	feedItems := feedData["items"].([]interface{})
	if len(feedItems) != 2 || feedData["control_context"] == nil {
		t.Fatalf("feed batch did not freeze items/context: %#v", feedData)
	}
	ugc := feedItems[0].(map[string]interface{})
	pgc := feedItems[1].(map[string]interface{})
	if ugc["author_identity"] == nil || pgc["author_identity"] != nil {
		t.Fatalf("UGC/PGC identity policy mismatch: ugc=%#v pgc=%#v", ugc, pgc)
	}
	status, ackPayload, _ := performJSON(t, h, "POST", "/api/v2/feed/batches/"+feedData["batch_id"].(string)+"/ack", ackFeedBatchRequest{
		LeaseEpoch: int64(feedData["lease_epoch"].(float64)), LeaseToken: feedData["lease_token"].(string),
		IdempotencyKey: "ack-" + agentID,
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != 200 || responseData(t, ackPayload)["status"] != "acked" {
		t.Fatalf("feed ack status=%d payload=%#v", status, ackPayload)
	}

	status, commandPayload, _ := performJSON(t, h, "POST", "/api/v2/agent-commands", createAgentCommandRequest{
		CommandType: "human_instruction", Payload: json.RawMessage(`{"instruction":"review the new signal"}`),
		IdempotencyKey: "command-" + agentID,
	}, ut.Header{Key: "Cookie", Value: cookieHeader}, ut.Header{Key: "X-CSRF-Token", Value: csrf})
	if status != 201 {
		t.Fatalf("command create status=%d payload=%#v", status, commandPayload)
	}
	commandID := responseData(t, commandPayload)["command_id"].(string)
	status, claimPayload, _ := performJSON(t, h, "POST", "/api/v2/agent-commands/"+commandID+"/claim", claimAgentCommandRequest{
		RuntimeInstanceID: "integration-runtime", AppliedContextRevision: contextRevision,
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != 200 {
		t.Fatalf("command claim status=%d payload=%#v", status, claimPayload)
	}
	claimData := responseData(t, claimPayload)
	status, completePayload, _ := performJSON(t, h, "POST", "/api/v2/agent-commands/"+commandID+"/complete", completeAgentCommandRequest{
		RuntimeInstanceID: "integration-runtime", ClaimEpoch: int64(claimData["claim_epoch"].(float64)),
		ClaimToken: claimData["claim_token"].(string), Status: "completed", Result: json.RawMessage(`{"handled":true}`),
	}, ut.Header{Key: "Authorization", Value: "Bearer " + accessToken})
	if status != 200 || responseData(t, completePayload)["status"] != "completed" {
		t.Fatalf("command complete status=%d payload=%#v", status, completePayload)
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

func testCommunicationProjection(t *testing.T, gdb *gorm.DB, h *server.Hertz, idgen *fixedIDGenerator, viewerID int64, cookieHeader string) {
	t.Helper()
	now := time.Now().UnixMilli()
	peerID, _ := idgen.NextID()
	requestPeerID, _ := idgen.NextID()
	convID, _ := idgen.NextID()
	msgID, _ := idgen.NextID()
	requestID, _ := idgen.NextID()
	foreignConvID, _ := idgen.NextID()
	publicCard := `{"agent_description":"Public Agent description","human_description":"Public human description","working_languages":["zh","en"],"seeking":["signals"],"offering":["analysis"]}`
	privateCard := `{"current_focus":["PRIVATE_FOCUS_MUST_NOT_LEAK"],"human_status":["PRIVATE_STATUS_MUST_NOT_LEAK"]}`
	if err := gdb.Exec(`INSERT INTO agents (agent_id, email, agent_name, bio, created_at, updated_at, is_official)
		VALUES (?, ?, 'Official Peer', 'peer bio', ?, ?, true),
		       (?, ?, 'Request Peer', 'request bio', ?, ?, false)`,
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
		       (?, ?, ?, ?, ?, 'broadcast', 0, 0, ?)`,
		convID, viewerID, peerID, viewerID, peerID, now,
		foreignConvID, peerID, requestPeerID, peerID, requestPeerID, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO private_messages
		(msg_id, conv_id, sender_id, receiver_id, content, is_read, created_at)
		VALUES (?, ?, ?, ?, 'hello from peer', false, ?)`, msgID, convID, peerID, viewerID, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO friend_requests
		(id, from_uid, to_uid, status, greeting, remark, created_at, updated_at)
		VALUES (?, ?, ?, 0, 'hello', '', ?, ?)`, requestID, viewerID, requestPeerID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		gdb.Exec(`DELETE FROM private_messages WHERE conv_id IN (?, ?)`, convID, foreignConvID)
		gdb.Exec(`DELETE FROM conversations WHERE conv_id IN (?, ?)`, convID, foreignConvID)
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
	if len(conversations) != 1 || conversations[0].(map[string]interface{})["unread_count"] != float64(1) || conversations[0].(map[string]interface{})["last_message"] == nil {
		t.Fatalf("conversation batch enrichment mismatch: %#v", conversations)
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
