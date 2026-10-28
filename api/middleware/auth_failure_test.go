package middleware

import (
	"bytes"
	"context"
	"eigenflux_server/api/clients"
	auth "eigenflux_server/kitex_gen/eigenflux/auth"
	"eigenflux_server/kitex_gen/eigenflux/auth/authservice"
	"eigenflux_server/kitex_gen/eigenflux/base"
	"encoding/json"
	"errors"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/kitex/client/callopt"
	"log/slog"
	"strings"
	"testing"
)

type rejectingAuth struct {
	authservice.Client
	code int32
	err  error
}

func (f rejectingAuth) ValidateSession(context.Context, *auth.ValidateSessionReq, ...callopt.Option) (*auth.ValidateSessionResp, error) {
	return &auth.ValidateSessionResp{BaseResp: &base.BaseResp{Code: f.code, Msg: "password=rpc-secret"}}, f.err
}
func TestBusinessAuthFailureRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, header, reason string
		rpc                  int32
		err                  error
		status               int
	}{
		{"missing", "", "missing_authorization_header", 0, nil, 401},
		{"invalid", "Bearer token-secret", "credential_invalid_or_expired", 401, nil, 401},
		{"other rejection", "Bearer token-secret", "auth_rpc_rejected", 503, nil, 401},
		{"rpc failure", "Bearer token-secret", "auth_rpc_unavailable", 0, errors.New("password=rpc-secret"), 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := clients.AuthClient
			clients.AuthClient = rejectingAuth{code: tc.rpc, err: tc.err}
			defer func() { clients.AuthClient = old }()
			h := server.New()
			h.GET("/protected/:id", AuthMiddleware())
			var logs bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(prev)
			res := ut.PerformRequest(h.Engine, "GET", "/protected/private?secret=query-secret", nil, ut.Header{Key: "Authorization", Value: tc.header})
			if res.Code != tc.status {
				t.Fatalf("status=%d", res.Code)
			}
			var row map[string]interface{}
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &row); err != nil {
				t.Fatal(err, logs.String())
			}
			if row["reason"] != tc.reason || row["stage"] != "legacy_access" || row["route"] != "/protected/:id" {
				t.Fatalf("bad log %#v", row)
			}
			for _, secret := range []string{"token-secret", "rpc-secret", "password=", "query-secret", "/protected/private"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("leaked %s", secret)
				}
			}
		})
	}
}
