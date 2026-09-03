package commissionaccess

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

func TestNewNormalizesValidAgentIDs(t *testing.T) {
	access, err := New(" 42, 42, ,9223372036854775807,")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{42, 9223372036854775807} {
		if !access.allowed(id) {
			t.Fatalf("Agent %d was not allowed", id)
		}
	}
	if access.allowed(41) {
		t.Fatal("unconfigured Agent was allowed")
	}
}

func TestNewRejectsInvalidAgentIDs(t *testing.T) {
	for _, raw := range []string{"0", "-1", "agent-42", "9223372036854775808"} {
		t.Run(raw, func(t *testing.T) {
			if access, err := New(raw); err == nil || access != nil {
				t.Fatalf("New(%q) access=%v error=%v", raw, access, err)
			}
		})
	}
}

func TestNewEmptyAllowlistDeniesEveryAgent(t *testing.T) {
	for _, raw := range []string{"", " , , "} {
		access, err := New(raw)
		if err != nil {
			t.Fatal(err)
		}
		if access.allowed(42) {
			t.Fatalf("New(%q) allowed Agent 42", raw)
		}
	}
}

func performGate(t *testing.T, gate app.HandlerFunc, agentID int64) (int, map[string]any, bool, string) {
	t.Helper()
	h := server.New()
	called := false
	h.GET("/test",
		func(ctx context.Context, c *app.RequestContext) {
			if agentID > 0 {
				c.Set("agent_id", agentID)
			}
			c.Next(ctx)
		},
		gate,
		func(_ context.Context, c *app.RequestContext) {
			called = true
			c.JSON(http.StatusOK, map[string]any{"ok": true})
		},
	)
	recorder := ut.PerformRequest(h.Engine, http.MethodGet, "/test", nil)
	var payload map[string]any
	if err := json.Unmarshal(recorder.Result().Body(), &payload); err != nil {
		t.Fatal(err)
	}
	return recorder.Result().StatusCode(), payload, called, string(recorder.Result().Header.Peek("Cache-Control"))
}

func TestV1MiddlewareRejectsUnlistedAgent(t *testing.T) {
	access, _ := New("7")
	status, payload, called, _ := performGate(t, access.V1Middleware(), 8)
	if status != http.StatusForbidden || payload["code"] != float64(403) || payload["msg"] != "commission access is not allowed" || called {
		t.Fatalf("status=%d payload=%v called=%v", status, payload, called)
	}
}

func TestConsoleMiddlewareRejectsUnlistedAgent(t *testing.T) {
	access, _ := New("7")
	status, payload, called, cacheControl := performGate(t, access.ConsoleMiddleware(), 8)
	errorPayload := payload["error"].(map[string]any)
	if status != http.StatusForbidden || errorPayload["code"] != "COMMISSION_ACCESS_FORBIDDEN" || called {
		t.Fatalf("status=%d payload=%v called=%v", status, payload, called)
	}
	if cacheControl != "private, no-store" {
		t.Fatalf("Cache-Control=%q", cacheControl)
	}
}

func TestMiddlewareAllowsListedAgent(t *testing.T) {
	access, _ := New("7")
	for name, gate := range map[string]app.HandlerFunc{"v1": access.V1Middleware(), "console": access.ConsoleMiddleware()} {
		t.Run(name, func(t *testing.T) {
			status, _, called, _ := performGate(t, gate, 7)
			if status != http.StatusOK || !called {
				t.Fatalf("status=%d called=%v", status, called)
			}
		})
	}
}
