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

func TestAfterSalesDelegatesOnlyExactBoundOrder(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.RequestURI() != "/api/v1/orders/9007199254740993/after-sales" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		claims := tokenClaims(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if claims.Subject != "7" || claims.Scope != "orders:write" || claims.Operation != "console.trade.orders.after_sales.create" {
			t.Errorf("unexpected delegation: %+v", claims)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{"kind":"refund","reason":"missing file","order_id":"9007199254740993"}` || r.Header.Get("Idempotency-Key") != "after-sales-test" {
			t.Errorf("unexpected bound request: %s (%v)", body, err)
		}
		io.WriteString(w, `{"code":0,"data":{"order":{"contract":{"seller_net_fen":1}}}}`)
	}))
	defer server.Close()
	service := configuredService(t, server.URL)
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"kind":"refund","reason":"missing file"}`, http.StatusOK},
		{`{"kind":"refund","reason":"missing file","order_id":"9"}`, http.StatusBadRequest},
		{`{"kind":"refund","kind":"complaint","reason":"missing file"}`, http.StatusBadRequest},
	} {
		c := app.NewContext(0)
		c.Params = param.Params{{Key: "order_id", Value: "9007199254740993"}}
		c.Set("agent_id", int64(7))
		c.Request.Header.Set("Idempotency-Key", "after-sales-test")
		c.Request.SetBodyString(tc.body)
		service.TradeOrderAfterSales(context.Background(), c)
		if c.Response.StatusCode() != tc.status || strings.Contains(string(c.Response.Body()), "seller_net_fen") {
			t.Fatalf("status %d, response %s", c.Response.StatusCode(), c.Response.Body())
		}
	}
	if calls != 1 {
		t.Fatalf("unexpected upstream call count %d", calls)
	}
}
