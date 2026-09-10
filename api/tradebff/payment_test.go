package tradebff

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/route/param"
)

func paymentContext(orderID, body, key string, authenticated bool) *app.RequestContext {
	c := app.NewContext(1)
	c.Params = param.Params{{Key: "order_id", Value: orderID}}
	c.Request.Header.SetMethod(http.MethodPost)
	c.Request.Header.Set("Idempotency-Key", key)
	c.Request.SetBodyString(body)
	if authenticated {
		c.Set("agent_id", int64(42))
	}
	return c
}

func TestTradeOrderPaymentBindsCanonicalOrderAndAuthenticatedSubject(t *testing.T) {
	var authorization, receivedKey, path, method string
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization, receivedKey = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		path, method = r.URL.RequestURI(), r.Method
		receivedBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"payment_action":{"provider":"alipay","type":"redirect","url":"https://openapi.alipay.com/gateway.do?test=1","expires_at":"2026-09-07T12:00:00Z"}}}`)
	}))
	defer server.Close()
	c := paymentContext("9007199254740993", `{"channel":"wap"}`, "payment-1", true)
	configuredService(t, server.URL).TradeOrderPayment(context.Background(), c)
	if c.Response.StatusCode() != http.StatusOK || path != "/api/v1/orders/9007199254740993/payment" || method != http.MethodPost {
		t.Fatalf("status=%d method=%s path=%s body=%s", c.Response.StatusCode(), method, path, c.Response.Body())
	}
	if string(receivedBody) != `{"channel":"wap","order_id":"9007199254740993"}` || receivedKey != "payment-1" {
		t.Fatalf("body=%s key=%q", receivedBody, receivedKey)
	}
	claims := tokenClaims(t, strings.TrimPrefix(authorization, "Bearer "))
	if claims.Subject != "42" || claims.Scope != "orders:write" || claims.Operation != "console.trade.orders.payment" || claims.Method != http.MethodPost ||
		claims.BodySHA256 != delegationDigest(receivedBody) || claims.IdempotencyKeySHA256 != delegationDigest([]byte(receivedKey)) {
		t.Fatalf("invalid binding: %#v", claims)
	}
	if string(c.Response.Header.Peek("Cache-Control")) != "private, no-store" || !strings.Contains(string(c.Response.Body()), `"payment_action"`) {
		t.Fatalf("missing no-store/payment action: %s", c.Response.Body())
	}
}

func TestTradeOrderPaymentRejectsUntrustedInputBeforeCommission(t *testing.T) {
	for _, tc := range []struct {
		name, id, body, key string
		auth                bool
		status              int
	}{
		{"missing session", "1", `{"channel":"wap"}`, "key", false, 401},
		{"missing key", "1", `{"channel":"wap"}`, "", true, 400},
		{"blank key", "1", `{"channel":"wap"}`, "  ", true, 400},
		{"leading zero", "01", `{"channel":"wap"}`, "key", true, 400},
		{"whitespace id", " 1", `{"channel":"wap"}`, "key", true, 400},
		{"overflow", "9223372036854775808", `{"channel":"wap"}`, "key", true, 400},
		{"negative", "-1", `{"channel":"wap"}`, "key", true, 400},
		{"zero", "0", `{"channel":"wap"}`, "key", true, 400},
		{"path injection", "1/payment", `{"channel":"wap"}`, "key", true, 400},
		{"amount", "1", `{"channel":"wap","amount_fen":1}`, "key", true, 400},
		{"return url", "1", `{"channel":"wap","return_url":"https://evil.test"}`, "key", true, 400},
		{"browser order", "1", `{"channel":"wap","order_id":"2"}`, "key", true, 400},
		{"wrong channel", "1", `{"channel":"qr"}`, "key", true, 400},
		{"missing channel", "1", `{}`, "key", true, 400},
		{"null", "1", `null`, "key", true, 400},
		{"array", "1", `[]`, "key", true, 400},
		{"trailing value", "1", `{"channel":"wap"}{}`, "key", true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			defer server.Close()
			c := paymentContext(tc.id, tc.body, tc.key, tc.auth)
			configuredService(t, server.URL).TradeOrderPayment(context.Background(), c)
			if c.Response.StatusCode() != tc.status || called {
				t.Fatalf("status=%d called=%v body=%s", c.Response.StatusCode(), called, c.Response.Body())
			}
		})
	}
}
