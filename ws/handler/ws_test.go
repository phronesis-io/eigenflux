package handler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/auth"
	"eigenflux_server/kitex_gen/eigenflux/auth/authservice"
	"eigenflux_server/kitex_gen/eigenflux/base"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/kitex/client/callopt"
)

type authResultClient struct {
	authservice.Client
	response *auth.ValidateSessionResp
	err      error
	token    string
}

func (f *authResultClient) ValidateSession(_ context.Context, req *auth.ValidateSessionReq, _ ...callopt.Option) (*auth.ValidateSessionResp, error) {
	f.token = req.AccessToken
	return f.response, f.err
}

func TestAgentV2HandshakePreservesAuthFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rpc    int32
		status int
		code   string
	}{
		{"invalid credential", 401, 401, "AGENT_AUTH_INVALID"},
		{"onboarding incomplete", 409, 409, "ONBOARDING_REQUIRED"},
		{"missing communication scope", 403, 403, "AGENT_SCOPE_REQUIRED"},
		{"database unavailable", 503, 503, "AGENT_AUTH_UNAVAILABLE"},
		{"unexpected service failure", 500, 503, "AGENT_AUTH_UNAVAILABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &authResultClient{response: &auth.ValidateSessionResp{BaseResp: &base.BaseResp{Code: tc.rpc}}}
			assertHandshakeError(t, client, "Bearer efv2a_test", tc.status, tc.code)
			if client.token != "efv2a_test" {
				t.Fatalf("RPC received token %q", client.token)
			}
		})
	}
}

func TestAgentV2HandshakeFailsClosedOnInvalidAuthResponse(t *testing.T) {
	for name, client := range map[string]*authResultClient{
		"RPC unavailable":  {err: errors.New("connection refused")},
		"nil response":     {},
		"nil status":       {response: &auth.ValidateSessionResp{AgentId: 42}},
		"missing identity": {response: &auth.ValidateSessionResp{BaseResp: &base.BaseResp{Code: 0}}},
	} {
		t.Run(name, func(t *testing.T) {
			assertHandshakeError(t, client, "Bearer efv2a_test", 503, "AGENT_AUTH_UNAVAILABLE")
		})
	}
}

func TestAgentV2HandshakeRejectsMissingBearerBeforeRPC(t *testing.T) {
	client := &authResultClient{}
	assertHandshakeError(t, client, "Bearer at_legacy", 401, "AGENT_AUTH_REQUIRED")
	if client.token != "" {
		t.Fatal("invalid V2 bearer reached the RPC validator")
	}
}

func assertHandshakeError(t *testing.T, client *authResultClient, bearer string, status int, code string) {
	t.Helper()
	h := server.New()
	handler := New(client, nil, nil)
	h.GET("/api/v2/agent/events/ws", handler.ServeAgentV2)
	response := ut.PerformRequest(h.Engine, "GET", "/api/v2/agent/events/ws", nil, ut.Header{Key: "Authorization", Value: bearer}).Result()
	if response.StatusCode() != status {
		t.Fatalf("status = %d, want %d; body = %s", response.StatusCode(), status, response.Body())
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != code || payload.Error.Message == "" {
		t.Fatalf("unexpected auth error: %s", response.Body())
	}
}
