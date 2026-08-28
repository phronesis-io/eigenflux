package tradebff

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
)

func TestUnavailableTradeOverviewDoesNotInventMoney(t *testing.T) {
	ctx := app.NewContext(0)
	ctx.Request.SetRequestURI("/api/v2/console/bff/trade/overview")
	ctx.Set("agent_id", int64(42))
	NewUnavailable("").TradeOverview(context.Background(), ctx)
	if ctx.Response.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", ctx.Response.StatusCode())
	}
	if strings.Contains(string(ctx.Response.Body()), "total_fen") || strings.Contains(string(ctx.Response.Body()), "withdrawable_fen") {
		t.Fatalf("unavailable response invented money: %s", ctx.Response.Body())
	}
}

func TestTradeCommissionsDelegatesAuthenticatedSubject(t *testing.T) {
	var authorization, requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization, requestPath = r.Header.Get("Authorization"), r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"items":[{"commission_id":"9007199254740993"}],"page":{"next_cursor":"22","total":1}}}`)
	}))
	defer server.Close()
	service := configuredService(t, server.URL)
	ctx := app.NewContext(0)
	ctx.Request.SetRequestURI("/api/v2/console/bff/trade/commissions?cursor=11&limit=20")
	ctx.Set("agent_id", int64(42))
	service.TradeCommissions(context.Background(), ctx)
	if ctx.Response.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if !strings.HasPrefix(authorization, "Bearer "+delegationPrefix+"current.") {
		t.Fatalf("authorization = %q", authorization)
	}
	if requestPath != "/api/v2/console/trade/commissions?cursor=11&limit=20" {
		t.Fatalf("request path = %q", requestPath)
	}
	claims := tokenClaims(t, strings.TrimPrefix(authorization, "Bearer "))
	if claims.Subject != "42" || claims.Scope != "commissions:mine:read" || claims.Operation != "console.trade.commissions.list" {
		t.Fatalf("claims = %#v", claims)
	}
}

func TestWithdrawalDelegationBindsBodyAndIdempotencyKey(t *testing.T) {
	var authorization, idempotency string
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization, idempotency = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		receivedBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"withdrawal":{"withdrawal_id":"9","amount_fen":3100}}}`)
	}))
	defer server.Close()
	service := configuredService(t, server.URL)
	ctx := app.NewContext(0)
	ctx.Request.SetRequestURI("/api/v2/console/bff/withdrawals")
	ctx.Request.Header.SetMethod(http.MethodPost)
	ctx.Request.Header.Set("Idempotency-Key", "idem-1")
	ctx.Request.SetBodyString(`{"amount_fen":3100}`)
	ctx.Set("agent_id", int64(42))
	service.CreateWithdrawal(context.Background(), ctx)
	if ctx.Response.StatusCode() != http.StatusOK || idempotency != "idem-1" {
		t.Fatalf("status=%d idempotency=%q body=%s", ctx.Response.StatusCode(), idempotency, ctx.Response.Body())
	}
	claims := tokenClaims(t, strings.TrimPrefix(authorization, "Bearer "))
	if claims.BodySHA256 != delegationDigest(receivedBody) || claims.IdempotencyKeySHA256 != delegationDigest([]byte("idem-1")) {
		t.Fatalf("mutation binding mismatch: %#v", claims)
	}
}

func TestWithdrawalRejectsMissingIdempotencyKeyBeforeCommission(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	service := configuredService(t, server.URL)
	ctx := app.NewContext(0)
	ctx.Request.SetRequestURI("/api/v2/console/bff/withdrawals")
	ctx.Request.Header.SetMethod(http.MethodPost)
	ctx.Request.SetBodyString(`{"amount_fen":3100}`)
	ctx.Set("agent_id", int64(42))
	service.CreateWithdrawal(context.Background(), ctx)
	if ctx.Response.StatusCode() != http.StatusBadRequest || called {
		t.Fatalf("status=%d called=%v body=%s", ctx.Response.StatusCode(), called, ctx.Response.Body())
	}
}

func configuredService(t *testing.T, endpoint string) *Service {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{Endpoint: endpoint, DelegationKeyID: "current", DelegationPrivateKey: base64.RawURLEncoding.EncodeToString(privateKey)})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func tokenClaims(t *testing.T, token string) delegationClaims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("invalid token %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims delegationClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}
