package tradebff

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
)

func TestWalletKYCRoutesBindActorAndProjectPrivateData(t *testing.T) {
	for _, operation := range []string{"read", "start", "authorize"} {
		t.Run(operation, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				claims := tokenClaims(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
				if claims.Subject != "42" || claims.Operation != "wallet.kyc."+operation {
					t.Fatalf("invalid claims: %#v", claims)
				}
				if operation != "read" {
					body, _ := io.ReadAll(r.Body)
					if claims.BodySHA256 != delegationDigest(body) || claims.IdempotencyKeySHA256 != delegationDigest([]byte("test-key")) {
						t.Fatal("mutation not bound")
					}
				}
				if r.Header.Get("X-Wallet-KYC-Browser") != "" {
					t.Fatal("delegated start must not request browser grant")
				}
				_, _ = io.WriteString(w, `{"code":0,"data":{"verification":{"verification_id":"9","binding_id":"8","state":"pending","cert_no":"private-id","verify_id":"private-provider-id","authorization_url":"https://www.eigenflux.ai/api/v1/public/wallet/kyc/launch?ticket=private-link","authorization_expires_at":123}}}`)
			}))
			defer server.Close()
			s := configuredService(t, server.URL)
			c := app.NewContext(0)
			c.Set("agent_id", int64(42))
			c.Request.Header.Set("Idempotency-Key", "test-key")
			switch operation {
			case "read":
				s.WalletKYC(context.Background(), c)
			case "start":
				c.Request.SetBodyString(`{"user_name":"Test","cert_no":"123456789012345678"}`)
				s.StartWalletKYC(context.Background(), c)
			case "authorize":
				c.Request.SetBodyString(`{"verification_id":"9"}`)
				s.AuthorizeWalletKYC(context.Background(), c)
			}
			body := string(c.Response.Body())
			if c.Response.StatusCode() != 200 || strings.Contains(body, "private-id") || strings.Contains(body, "private-provider-id") {
				t.Fatalf("unsafe response: %d %s", c.Response.StatusCode(), body)
			}
			if strings.Contains(body, "private-link") != (operation == "authorize") {
				t.Fatalf("unexpected link projection: %s", body)
			}
		})
	}
}

func TestWalletKYCRejectsActorInjectionAndMissingKey(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	s := configuredService(t, server.URL)
	for _, body := range []string{`{"user_name":"Test","cert_no":"123456789012345678","agent_id":"99"}`, `{"user_name":"Test","cert_no":"123456789012345678"}`} {
		c := app.NewContext(0)
		c.Set("agent_id", int64(42))
		c.Request.SetBodyString(body)
		s.StartWalletKYC(context.Background(), c)
		if c.Response.StatusCode() != 400 || called {
			t.Fatal("invalid KYC reached upstream")
		}
	}
}

func TestWalletErrorProjectionPreservesSafeCode(t *testing.T) {
	c := app.NewContext(0)
	replyWalletError(c, &UpstreamError{Status: 422, ErrorCode: "WALLET_KYC_REQUIRED", Msg: "private-provider-diagnostic"})
	if c.Response.StatusCode() != 422 || !strings.Contains(string(c.Response.Body()), "WALLET_KYC_REQUIRED") || strings.Contains(string(c.Response.Body()), "private-provider") {
		t.Fatalf("unsafe error: %s", c.Response.Body())
	}
}
