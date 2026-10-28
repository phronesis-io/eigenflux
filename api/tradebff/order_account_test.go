package tradebff

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/route/param"
)

func orderAccountContext(expected string, authenticatedID int64) *app.RequestContext {
	c := app.NewContext(1)
	c.Params = param.Params{{Key: "order_id", Value: "9007199254740993"}}
	c.Request.Header.SetMethod(http.MethodGet)
	if expected != "" {
		c.Request.Header.Set("X-EigenFlux-Expected-Agent-ID", expected)
	}
	if authenticatedID > 0 {
		c.Set("agent_id", authenticatedID)
	}
	return c
}

func TestTradeOrderExpectedAccountRejectsBeforeCommission(t *testing.T) {
	for _, tc := range []struct {
		name, expected string
		agentID        int64
		status         int
		code           string
	}{
		{"different account", "43", 42, http.StatusForbidden, "EXPECTED_AGENT_MISMATCH"},
		{"adjacent large IDs", "9007199254740993", 9007199254740992, http.StatusForbidden, "EXPECTED_AGENT_MISMATCH"},
		{"no authenticated account", "42", 0, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED"},
		{"no header or session", "", 0, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED"},
		{"zero", "0", 42, http.StatusBadRequest, "INVALID_EXPECTED_AGENT_ID"},
		{"negative", "-42", 42, http.StatusBadRequest, "INVALID_EXPECTED_AGENT_ID"},
		{"leading zero", "042", 42, http.StatusBadRequest, "INVALID_EXPECTED_AGENT_ID"},
		{"plus sign", "+42", 42, http.StatusBadRequest, "INVALID_EXPECTED_AGENT_ID"},
		{"leading whitespace", " 42", 42, http.StatusBadRequest, "INVALID_EXPECTED_AGENT_ID"},
		{"trailing whitespace", "42 ", 42, http.StatusBadRequest, "INVALID_EXPECTED_AGENT_ID"},
		{"exponent", "4.2e1", 42, http.StatusBadRequest, "INVALID_EXPECTED_AGENT_ID"},
		{"overflow", "9223372036854775808", 42, http.StatusBadRequest, "INVALID_EXPECTED_AGENT_ID"},
		{"comma separated", "42,43", 42, http.StatusBadRequest, "INVALID_EXPECTED_AGENT_ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer upstream.Close()
			c := orderAccountContext(tc.expected, tc.agentID)
			configuredService(t, upstream.URL).TradeOrder(context.Background(), c)
			var response struct {
				Error struct{ Code string } `json:"error"`
			}
			if err := json.Unmarshal(c.Response.Body(), &response); err != nil {
				t.Fatal(err)
			}
			if c.Response.StatusCode() != tc.status || response.Error.Code != tc.code || calls.Load() != 0 {
				t.Fatalf("status=%d code=%s upstream_calls=%d body=%s", c.Response.StatusCode(), response.Error.Code, calls.Load(), c.Response.Body())
			}
			if string(c.Response.Header.Peek("Cache-Control")) != "private, no-store" {
				t.Fatal("account error must not be cached")
			}
		})
	}
}

func TestTradeOrderExpectedAccountPreservesAuthenticatedDelegation(t *testing.T) {
	for _, tc := range []struct {
		name, expected string
		agentID        int64
	}{
		{"legacy without header", "", 42},
		{"matching account", "42", 42},
		{"large account", "9007199254740993", 9007199254740993},
		{"maximum account", "9223372036854775807", 9223372036854775807},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type request struct{ authorization, path, expected string }
			received := make(chan request, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- request{r.Header.Get("Authorization"), r.URL.RequestURI(), r.Header.Get("X-EigenFlux-Expected-Agent-ID")}
				_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"order":{"order_id":"9007199254740993"}}}`)
			}))
			defer upstream.Close()
			c := orderAccountContext(tc.expected, tc.agentID)
			configuredService(t, upstream.URL).TradeOrder(context.Background(), c)
			if c.Response.StatusCode() != http.StatusOK {
				t.Fatalf("status=%d body=%s", c.Response.StatusCode(), c.Response.Body())
			}
			var got request
			select {
			case got = <-received:
			default:
				t.Fatal("order request was not delegated")
			}
			claims := tokenClaims(t, strings.TrimPrefix(got.authorization, "Bearer "))
			if got.path != "/api/v2/console/trade/orders/9007199254740993" || got.expected != "" ||
				claims.Subject != strconv.FormatInt(tc.agentID, 10) || claims.Scope != "orders:read" || claims.Operation != "console.trade.orders.get" {
				t.Fatalf("incorrect order delegation: path=%s forwarded_expected=%q claims=%+v", got.path, got.expected, claims)
			}
		})
	}
}
