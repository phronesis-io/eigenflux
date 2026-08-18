package main

import (
	"context"
	"net/http"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"

	"eigenflux_server/api/clients"
	profilerpc "eigenflux_server/kitex_gen/eigenflux/profile"
	"eigenflux_server/pkg/db"
)

// consoleV2GetMe keeps internal V2 aliases out of every browser DTO. It uses
// the same Profile RPC as V1 but owns a separate response projection so the V1
// contract and generated handler remain untouched.
func consoleV2GetMe(ctx context.Context, c *app.RequestContext) {
	value, ok := c.Get("agent_id")
	agentID, typeOK := value.(int64)
	if !ok || !typeOK || agentID <= 0 {
		c.JSON(http.StatusUnauthorized, map[string]interface{}{"code": 401, "msg": "unauthorized"})
		return
	}
	resp, err := clients.ProfileClient.GetAgent(ctx, &profilerpc.GetAgentReq{AgentId: agentID})
	if err != nil || resp == nil || resp.BaseResp == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]interface{}{"code": 503, "msg": "profile temporarily unavailable"})
		return
	}
	if resp.BaseResp.Code != 0 || resp.Agent == nil {
		c.JSON(http.StatusOK, map[string]interface{}{"code": resp.BaseResp.Code, "msg": resp.BaseResp.Msg})
		return
	}
	var identity struct {
		EmailKind string `gorm:"column:email_kind"`
	}
	_ = db.DB.Raw(`SELECT email_kind FROM agents WHERE agent_id = ?`, agentID).Scan(&identity).Error
	emailValue := resp.Agent.Email
	if identity.EmailKind == "internal_alias" {
		emailValue = ""
	}
	profile := map[string]interface{}{
		"agent_id": strconv.FormatInt(agentID, 10), "agent_name": resp.Agent.AgentName,
		"bio": resp.Agent.Bio, "email": emailValue, "created_at": resp.Agent.CreatedAt,
		"updated_at": resp.Agent.UpdatedAt,
	}
	if resp.Agent.Country != nil {
		profile["country"] = *resp.Agent.Country
	}
	if resp.Agent.Keywords != nil {
		profile["keywords"] = resp.Agent.Keywords
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, map[string]interface{}{
		"code": 0, "msg": "success", "data": map[string]interface{}{
			"profile": profile,
			"influence": map[string]interface{}{
				"total_items": resp.Influence.TotalItems, "total_consumed": resp.Influence.TotalConsumed,
				"total_scored_1": resp.Influence.TotalScored_1, "total_scored_2": resp.Influence.TotalScored_2,
			},
		},
	})
}
