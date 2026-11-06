package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type capturedRequest struct {
	Method string
	Path   string
	Auth   string
	Body   map[string]any
}

type recorder struct {
	mu       sync.Mutex
	requests []capturedRequest
}

func (r *recorder) add(req capturedRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
}

func (r *recorder) all() []capturedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]capturedRequest(nil), r.requests...)
}

func gateway(t *testing.T, handler func(w http.ResponseWriter, got capturedRequest)) (*GatewayPort, *recorder, *httptest.Server) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got := capturedRequest{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization")}
		if len(body) > 0 {
			_ = json.Unmarshal(body, &got.Body)
		}
		rec.add(got)
		w.Header().Set("Content-Type", "application/json")
		handler(w, got)
	}))
	t.Cleanup(srv.Close)
	return NewGatewayPort(srv.URL, map[string]string{"agent_alpha": "token-alpha"}), rec, srv
}

func ok(w http.ResponseWriter, data string) {
	_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":`+data+`}`)
}

func invitationBody() json.RawMessage {
	return json.RawMessage(`{"kind":"fluxspace.invitation.v1","invitation_id":"inv_1","proposals":[{"index":1}]}`)
}

func TestSendPMStringifiesStructuredJSONAndDeduplicates(t *testing.T) {
	port, rec, _ := gateway(t, func(w http.ResponseWriter, _ capturedRequest) {
		ok(w, `{"msg_id":"msg_1","conv_id":"conv_1"}`)
	})
	body := invitationBody()
	msgID, err := port.SendPM(context.Background(), "agent_alpha", "agent_beta", body)
	if err != nil {
		t.Fatalf("SendPM: %v", err)
	}
	if msgID != "msg_1" {
		t.Fatalf("msg_id = %q", msgID)
	}
	requests := rec.all()
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	got := requests[0]
	if got.Method != http.MethodPost || got.Path != "/api/v1/pm/send" {
		t.Fatalf("wrong endpoint: %s %s", got.Method, got.Path)
	}
	if got.Auth != "Bearer token-alpha" {
		t.Fatalf("auth = %q", got.Auth)
	}
	content, isString := got.Body["content"].(string)
	if !isString {
		t.Fatalf("content must be a JSON string per idl/api.thrift, got %T", got.Body["content"])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		t.Fatalf("content is not valid JSON: %v", err)
	}
	if decoded["kind"] != "fluxspace.invitation.v1" || decoded["invitation_id"] != "inv_1" {
		t.Fatalf("invitation lost in transit: %v", decoded)
	}
	if got.Body["receiver_id"] != "agent_beta" {
		t.Fatalf("receiver_id = %v", got.Body["receiver_id"])
	}
	firstID, _ := got.Body["request_id"].(string)
	if !strings.HasPrefix(firstID, "fluxspace-") {
		t.Fatalf("request_id = %q", firstID)
	}

	if _, err := port.SendPM(context.Background(), "agent_alpha", "agent_beta", body); err != nil {
		t.Fatalf("second SendPM: %v", err)
	}
	second := rec.all()[1]
	if second.Body["request_id"] != firstID {
		t.Fatal("request_id must be stable for an identical body so the gateway deduplicates retries")
	}
}

func TestSendPMRejectsInvalidBody(t *testing.T) {
	port, rec, _ := gateway(t, func(w http.ResponseWriter, _ capturedRequest) { ok(w, `{}`) })
	if _, err := port.SendPM(context.Background(), "agent_alpha", "agent_beta", json.RawMessage(`{nope`)); err == nil {
		t.Fatal("expected rejection of malformed JSON")
	}
	if len(rec.all()) != 0 {
		t.Fatal("invalid body must not reach the network")
	}
}

func TestSendPMPropagatesGatewayBusinessError(t *testing.T) {
	port, _, _ := gateway(t, func(w http.ResponseWriter, _ capturedRequest) {
		_, _ = io.WriteString(w, `{"code":1004,"msg":"friend relation required","data":{}}`)
	})
	_, err := port.SendPM(context.Background(), "agent_alpha", "agent_beta", invitationBody())
	if err == nil || !strings.Contains(err.Error(), "1004") || !strings.Contains(err.Error(), "friend relation required") {
		t.Fatalf("got %v", err)
	}
}

func TestListPMReturnsMessagesAndCursorIsUsed(t *testing.T) {
	port, rec, _ := gateway(t, func(w http.ResponseWriter, _ capturedRequest) {
		ok(w, `{"messages":[{"msg_id":"msg_9","conv_id":"conv_2","sender_id":"agent_beta","receiver_id":"agent_alpha","content":"{\"action\":\"accept\"}","is_read":false,"created_at":1730000000}],"next_cursor":"msg_9"}`)
	})
	messages, err := port.ListPM(context.Background(), "agent_alpha", "cursor_7")
	if err != nil {
		t.Fatalf("ListPM: %v", err)
	}
	if len(messages) != 1 || messages[0].MsgID != "msg_9" {
		t.Fatalf("messages = %+v", messages)
	}
	var reply struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(messages[0].Content, &reply); err != nil {
		t.Fatalf("PM content is not structured JSON: %s (%v)", messages[0].Content, err)
	}
	if reply.Action != "accept" {
		t.Fatalf("decoded reply = %+v", reply)
	}
	got := rec.all()[0]
	if got.Path != "/api/v1/pm/fetch" || got.Method != http.MethodGet {
		t.Fatalf("wrong endpoint: %s %s", got.Method, got.Path)
	}
}

func TestDoRotatesTokenOn401AndRetriesOnce(t *testing.T) {
	port, rec, _ := gateway(t, func(w http.ResponseWriter, got capturedRequest) {
		switch got.Path {
		case "/api/v1/pm/send":
			if got.Auth == "Bearer token-alpha" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"code":401,"msg":"expired"}`)
				return
			}
			ok(w, `{"msg_id":"msg_rotated","conv_id":"conv_3"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	// The client owns the refresh token and signing key; the Hub only receives
	// the resulting access token. G9 previously invented a /api/v1/auth/refresh
	// call that the real gateway never had.
	port.Refresher = RefresherFunc(func(context.Context, string) (string, error) {
		return "token-rotated", nil
	})
	msgID, err := port.SendPM(context.Background(), "agent_alpha", "agent_beta", invitationBody())
	if err != nil {
		t.Fatalf("SendPM after rotation: %v", err)
	}
	if msgID != "msg_rotated" {
		t.Fatalf("msg_id = %q", msgID)
	}
	var sends []string
	for _, r := range rec.all() {
		if r.Path == "/api/v1/pm/send" {
			sends = append(sends, r.Auth)
		}
	}
	if len(sends) != 2 || sends[0] != "Bearer token-alpha" || sends[1] != "Bearer token-rotated" {
		t.Fatalf("token rotation not applied to retry: %v", sends)
	}
	if port.token("agent_alpha") != "token-rotated" {
		t.Fatal("rotated token was not stored")
	}
}

func TestRefreshFailureIsReportedNotSilentlySwallowed(t *testing.T) {
	port, _, _ := gateway(t, func(w http.ResponseWriter, _ capturedRequest) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"code":401,"msg":"expired"}`)
	})
	port.Refresher = RefresherFunc(func(context.Context, string) (string, error) {
		return "", fmt.Errorf("refresh disabled")
	})
	_, err := port.SendPM(context.Background(), "agent_alpha", "agent_beta", invitationBody())
	if err == nil || !strings.Contains(err.Error(), "token refresh also failed") {
		t.Fatalf("got %v", err)
	}
}

func Test401WithoutRefresherFailsLoudly(t *testing.T) {
	port, rec, _ := gateway(t, func(w http.ResponseWriter, _ capturedRequest) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"code":401,"msg":"expired"}`)
	})
	_, err := port.SendPM(context.Background(), "agent_alpha", "agent_beta", invitationBody())
	if err == nil || !strings.Contains(err.Error(), "TOKEN_REFRESH_NOT_CONFIGURED") {
		t.Fatalf("got %v", err)
	}
	for _, r := range rec.all() {
		if strings.Contains(r.Path, "refresh") {
			t.Fatalf("must not call a nonexistent refresh endpoint, got %s", r.Path)
		}
	}
}

func TestSetAccessTokenIsUsedForSubsequentRequests(t *testing.T) {
	port, rec, _ := gateway(t, func(w http.ResponseWriter, _ capturedRequest) {
		ok(w, `{"msg_id":"msg_1"}`)
	})
	if err := port.SetAccessToken("agent_alpha", "token-from-client"); err != nil {
		t.Fatalf("SetAccessToken: %v", err)
	}
	if _, err := port.SendPM(context.Background(), "agent_alpha", "agent_beta", invitationBody()); err != nil {
		t.Fatalf("SendPM: %v", err)
	}
	if rec.all()[0].Auth != "Bearer token-from-client" {
		t.Fatalf("auth = %q", rec.all()[0].Auth)
	}
}

func TestEnsureFriendSendsFriendRequest(t *testing.T) {
	port, rec, _ := gateway(t, func(w http.ResponseWriter, _ capturedRequest) {
		ok(w, `{"request_id":"req_1"}`)
	})
	if err := port.EnsureFriend(context.Background(), "agent_alpha", "agent_beta"); err != nil {
		t.Fatalf("EnsureFriend: %v", err)
	}
	got := rec.all()[0]
	if got.Path != "/api/v1/relations/apply" || got.Body["to_uid"] != "agent_beta" {
		t.Fatalf("unexpected friend request: %+v", got)
	}
}

func TestPublishAgentCardFieldsUsesRealGatewayShape(t *testing.T) {
	port, rec, _ := gateway(t, func(w http.ResponseWriter, got capturedRequest) {
		switch got.Path {
		case "/api/v1/agents/me/card/refresh-context":
			ok(w, `{"profile_version":17,"editable_fields":{},"protected_paths":[]}`)
		case "/api/v1/agents/me/profile/fields":
			ok(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	// Verified against pkg/agentcard/registry.go: `game_preference` does not
	// exist, and string_list fields must be JSON arrays.
	err := port.PublishAgentCardFields(context.Background(), "agent_alpha", map[string]string{
		"current_focus":     "deduction theatre with Lin Shen",
		"agent_description": "Watches cooperative deduction matches.",
	})
	if err != nil {
		t.Fatalf("PublishAgentCardFields: %v", err)
	}
	requests := rec.all()
	var fieldsReq *capturedRequest
	for i := range requests {
		if requests[i].Path == "/api/v1/agents/me/profile/fields" {
			fieldsReq = &requests[i]
			break
		}
	}
	if fieldsReq == nil {
		t.Fatal("fields endpoint never called")
	}
	if fieldsReq.Method != http.MethodPut {
		t.Fatalf("method = %s, want PUT", fieldsReq.Method)
	}
	if fieldsReq.Body["expected_version"] != float64(17) {
		t.Fatalf("expected_version = %v", fieldsReq.Body["expected_version"])
	}
	if fieldsReq.Body["source"] != "fluxspace_hub" {
		t.Fatalf("source = %v", fieldsReq.Body["source"])
	}
	updates, ok := fieldsReq.Body["updates"].(map[string]any)
	if !ok {
		t.Fatalf("updates missing: %v", fieldsReq.Body)
	}
	focus, ok := updates["current_focus"].([]any)
	if !ok || len(focus) != 1 || focus[0] != "deduction theatre with Lin Shen" {
		t.Fatalf("current_focus must be a string_list: %v", updates["current_focus"])
	}
	if updates["agent_description"] != "Watches cooperative deduction matches." {
		t.Fatalf("agent_description = %v", updates["agent_description"])
	}
	if _, leaked := fieldsReq.Body["fields"]; leaked {
		t.Fatal("legacy `fields` key must not be sent; gateway expects `updates`")
	}
}

func TestPublishAgentCardFieldsRejectsUnknownFieldLocally(t *testing.T) {
	port, rec, _ := gateway(t, func(w http.ResponseWriter, _ capturedRequest) { ok(w, `{}`) })
	err := port.PublishAgentCardFields(context.Background(), "agent_alpha", map[string]string{
		"game_preference": "deduction",
	})
	if err == nil || !strings.Contains(err.Error(), "not an editable Agent Card field") {
		t.Fatalf("got %v", err)
	}
	if len(rec.all()) != 0 {
		t.Fatal("unknown fields must not reach the gateway")
	}
}

func TestPublishAgentCardFieldsRequiresProfileVersion(t *testing.T) {
	port, rec, _ := gateway(t, func(w http.ResponseWriter, _ capturedRequest) {
		ok(w, `{"version":17}`) // legacy/wrong shape
	})
	err := port.PublishAgentCardFields(context.Background(), "agent_alpha", map[string]string{
		"current_focus": "deduction",
	})
	if err == nil || !strings.Contains(err.Error(), "profile_version") {
		t.Fatalf("got %v", err)
	}
	if len(rec.all()) != 1 {
		t.Fatalf("must stop after the context read, got %d requests", len(rec.all()))
	}
}
