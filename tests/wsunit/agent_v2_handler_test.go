package wsunit

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/kitex/client/callopt"

	"eigenflux_server/kitex_gen/eigenflux/auth"
	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/ws/handler"
)

type authClientStub struct {
	validate func(context.Context, *auth.ValidateSessionReq) (*auth.ValidateSessionResp, error)
}

func (s *authClientStub) StartLogin(context.Context, *auth.StartLoginReq, ...callopt.Option) (*auth.StartLoginResp, error) {
	return nil, errors.New("unexpected StartLogin call")
}

func (s *authClientStub) VerifyLogin(context.Context, *auth.VerifyLoginReq, ...callopt.Option) (*auth.VerifyLoginResp, error) {
	return nil, errors.New("unexpected VerifyLogin call")
}

func (s *authClientStub) ValidateSession(ctx context.Context, req *auth.ValidateSessionReq, _ ...callopt.Option) (*auth.ValidateSessionResp, error) {
	if s.validate == nil {
		return nil, errors.New("unexpected ValidateSession call")
	}
	return s.validate(ctx, req)
}

func (s *authClientStub) Logout(context.Context, *auth.LogoutReq, ...callopt.Option) (*auth.LogoutResp, error) {
	return nil, errors.New("unexpected Logout call")
}

func newAgentV2Server(authClient *authClientStub) *server.Hertz {
	h := server.Default()
	wsHandler := handler.New(authClient, nil, nil)
	h.GET("/api/v2/agent/events/ws", wsHandler.ServeAgentV2)
	return h
}

func performAgentV2Request(h *server.Hertz, authorization string) int {
	var headers []ut.Header
	if authorization != "" {
		headers = append(headers, ut.Header{Key: "Authorization", Value: authorization})
	}
	return ut.PerformRequest(h.Engine, http.MethodGet, "/api/v2/agent/events/ws", nil, headers...).Result().StatusCode()
}

func TestServeAgentV2RejectsMissingOrInvalidAuthorization(t *testing.T) {
	for name, authorization := range map[string]string{
		"missing":            "",
		"legacy bearer":      "Bearer at_legacy",
		"wrong scheme":       "Basic efv2a_access",
		"wrong token prefix": "Bearer efv2_access",
	} {
		t.Run(name, func(t *testing.T) {
			validateCalls := 0
			h := newAgentV2Server(&authClientStub{validate: func(context.Context, *auth.ValidateSessionReq) (*auth.ValidateSessionResp, error) {
				validateCalls++
				return nil, errors.New("invalid authorization must not reach Auth RPC")
			}})

			if status := performAgentV2Request(h, authorization); status != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
			}
			if validateCalls != 0 {
				t.Fatalf("ValidateSession calls = %d, want 0", validateCalls)
			}
		})
	}
}

func TestServeAgentV2PassesBearerTokenToAuthRPC(t *testing.T) {
	const token = "efv2a_access-token"
	var receivedToken string
	h := newAgentV2Server(&authClientStub{validate: func(_ context.Context, req *auth.ValidateSessionReq) (*auth.ValidateSessionResp, error) {
		receivedToken = req.AccessToken
		return &auth.ValidateSessionResp{
			AgentId:  42,
			BaseResp: &base.BaseResp{},
		}, nil
	}})

	// The request intentionally omits WebSocket handshake headers. A 400 response
	// proves authentication succeeded and processing reached the upgrade boundary.
	if status := performAgentV2Request(h, "Bearer "+token); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if receivedToken != token {
		t.Fatalf("ValidateSession access token = %q, want %q", receivedToken, token)
	}
}

func TestServeAgentV2ReturnsUnauthorizedWhenAuthRPCRejectsSession(t *testing.T) {
	const token = "efv2a_rejected-token"
	validateCalls := 0
	h := newAgentV2Server(&authClientStub{validate: func(_ context.Context, req *auth.ValidateSessionReq) (*auth.ValidateSessionResp, error) {
		validateCalls++
		if req.AccessToken != token {
			t.Fatalf("ValidateSession access token = %q, want %q", req.AccessToken, token)
		}
		return &auth.ValidateSessionResp{
			BaseResp: &base.BaseResp{Code: 401, Msg: "invalid session"},
		}, nil
	}})

	if status := performAgentV2Request(h, "Bearer "+token); status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
	}
	if validateCalls != 1 {
		t.Fatalf("ValidateSession calls = %d, want 1", validateCalls)
	}
}
