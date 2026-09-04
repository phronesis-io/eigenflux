// Package commissionaccess restricts Commission-backed API routes to configured Agents.
package commissionaccess

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

type Allowlist struct {
	enabled  bool
	agentIDs map[int64]struct{}
}

func New(enabled bool, raw string) (*Allowlist, error) {
	if !enabled {
		return &Allowlist{}, nil
	}
	agentIDs := make(map[int64]struct{})
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		agentID, err := strconv.ParseInt(entry, 10, 64)
		if err != nil || agentID <= 0 {
			return nil, fmt.Errorf("invalid COMMISSION_AGENT_ID_WHITELIST")
		}
		agentIDs[agentID] = struct{}{}
	}
	return &Allowlist{enabled: true, agentIDs: agentIDs}, nil
}

func (a *Allowlist) allowed(agentID int64) bool {
	if a == nil || agentID <= 0 {
		return false
	}
	_, ok := a.agentIDs[agentID]
	return ok
}

func contextAgentID(c *app.RequestContext) (int64, bool) {
	value, exists := c.Get("agent_id")
	agentID, ok := value.(int64)
	return agentID, exists && ok && agentID > 0
}

func (a *Allowlist) V1Middleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if a != nil && !a.enabled {
			c.Next(ctx)
			return
		}
		agentID, ok := contextAgentID(c)
		if !ok || !a.allowed(agentID) {
			c.JSON(http.StatusForbidden, map[string]any{"code": 403, "msg": "commission access is not allowed"})
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}

func (a *Allowlist) ConsoleMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if a != nil && !a.enabled {
			c.Next(ctx)
			return
		}
		agentID, ok := contextAgentID(c)
		if !ok || !a.allowed(agentID) {
			c.Header("Cache-Control", "private, no-store")
			c.JSON(http.StatusForbidden, map[string]any{"error": map[string]any{
				"code":    "COMMISSION_ACCESS_FORBIDDEN",
				"message": "Commission access is not enabled for this Agent",
			}})
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}
