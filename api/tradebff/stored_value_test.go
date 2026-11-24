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

func TestAutoPayLimitBindsConsoleActorBodyAndKey(t *testing.T) {
	const body = `{"auto_pay_limit_fen":75}`
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		got, err := io.ReadAll(r.Body)
		claims := tokenClaims(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil || string(got) != body || r.Method != "PUT" || r.URL.Path != "/api/v1/wallet/auto-pay-limit" || claims.Subject != "42" || claims.Scope != "wallet:manage" || claims.Operation != "wallet.auto_pay_limit.update" || claims.BodySHA256 != delegationDigest(got) || claims.IdempotencyKeySHA256 != delegationDigest([]byte("limit-1")) {
			t.Errorf("unexpected limit delegation")
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"auto_pay_limit_fen":75}}`)
	}))
	defer srv.Close()
	service := configuredService(t, srv.URL)
	for _, key := range []string{"", "limit-1"} {
		c := app.NewContext(0)
		c.Set("agent_id", int64(42))
		c.Request.SetBodyString(body)
		c.Request.Header.Set("Idempotency-Key", key)
		service.SetAutoPayLimit(context.Background(), c)
		want := 200
		if key == "" {
			want = 400
		}
		if c.Response.StatusCode() != want {
			t.Fatalf("got %d want %d", c.Response.StatusCode(), want)
		}
	}
	if hits != 1 {
		t.Fatalf("got %d upstream calls", hits)
	}
}
