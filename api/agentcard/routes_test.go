package agentcardapi

import (
	"context"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

// TestRegisterCoexistsWithStaticAgentRoutes guards the one routing risk this
// package introduces: /api/v1/agents/:agent_id/card is the first parameter
// route under /api/v1/agents, which already has static siblings (me, profile,
// items) from the generated router. Hertz resolves static > param, but a
// conflict would panic at registration — this test catches that at CI time
// instead of at gateway boot.
func TestRegisterCoexistsWithStaticAgentRoutes(t *testing.T) {
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	noop := func(_ context.Context, _ *app.RequestContext) {}

	// The static routes the generated router + main.go register today.
	h.GET("/api/v1/agents/me", noop)
	h.PUT("/api/v1/agents/profile", noop)
	h.GET("/api/v1/agents/items", noop)
	h.GET("/api/v1/agents/me/settings", noop)
	h.PUT("/api/v1/agents/me/settings", noop)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("route registration panicked: %v", r)
		}
	}()
	Register(h)
	RegisterPublic(h)
}
