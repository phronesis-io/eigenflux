package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"eigenflux_server/api/dal"
	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/reqinfo"
	"eigenflux_server/pkg/runtimeidentity"
	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"
)

// agentActivityRoute is an explicit allowlist of actions initiated by Agents.
// Console views, metadata reads, passive delivery, and server events are absent.
func agentActivityRoute(method, path string) bool {
	switch method {
	case http.MethodGet:
		switch path {
		case "/api/v1/items/feed", "/api/v1/pm/fetch", "/api/v2/pm/fetch", "/api/v2/pm/messages":
			return true
		}
	case http.MethodPost:
		switch path {
		case "/api/v1/items/publish", "/api/v1/items/feedback", "/api/v1/items/events",
			"/api/v1/pm/send", "/api/v1/pm/close", "/api/v1/pm/topic-status",
			"/api/v1/relations/apply", "/api/v1/relations/handle", "/api/v1/relations/unfriend", "/api/v1/relations/block", "/api/v1/relations/unblock", "/api/v1/relations/remark",
			"/api/v2/feed", "/api/v2/feed/feedback", "/api/v2/feed/events:batch", "/api/v2/runtime/heartbeat", "/api/v2/notifications/ack",
			"/api/v2/broadcasts", "/api/v2/items/publish", "/api/v2/items/feedback", "/api/v2/items/events",
			"/api/v2/pm/messages", "/api/v2/pm/send", "/api/v2/pm/close", "/api/v2/pm/conversations/close", "/api/v2/pm/conversations/topic-status",
			"/api/v2/relations/apply", "/api/v2/relations/handle", "/api/v2/relations/unfriend", "/api/v2/relations/block", "/api/v2/relations/unblock", "/api/v2/relations/remark",
			"/api/v2/relations/friend-requests", "/api/v2/relations/friend-requests/handle", "/api/v2/relations/friends/unfriend", "/api/v2/relations/friends/block", "/api/v2/relations/friends/unblock", "/api/v2/relations/friends/remark",
			"/api/v2/agent-attention-items/prefill", "/api/v2/agent-attention-items:publish", "/api/v2/agent-attention-items/:attention_id/respond", "/api/v2/agent-attention-items/:attention_id/dismiss",
			"/api/v2/agent-commands/:command_id/claim", "/api/v2/agent-commands/:command_id/complete", "/api/v2/agent-context/intent-actions":
			return true
		}
	case http.MethodDelete:
		switch path {
		case "/api/v1/agents/items/:item_id", "/api/v2/broadcasts/:item_id", "/api/v2/agents/items/:item_id", "/api/v2/agent-context/intent-actions/:intent_id":
			return true
		}
	case http.MethodPut:
		switch path {
		case "/api/v2/agent-settings/heartbeat-compatibility", "/api/v1/agents/profile", "/api/v1/agents/me/profile/fields", "/api/v2/agent-profile/fields",
			"/api/v2/agent-context/network-goal", "/api/v2/agent-context/intent-actions/:intent_id", "/api/v2/agent-context/security-boundary":
			return true
		}
	}
	return false
}

func successfulAgentResponse(c *app.RequestContext) bool {
	if c.Response.StatusCode() < 200 || c.Response.StatusCode() >= 300 {
		return false
	}
	var envelope struct {
		Code  *int            `json:"code"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(c.Response.Body(), &envelope); err != nil {
		return false
	}
	return (envelope.Code == nil || *envelope.Code == 0) && (len(envelope.Error) == 0 || string(envelope.Error) == "null")
}

// ObserveSuccessfulAgentRequest runs only after Agent authentication and a
// successful allowlisted handler. V1 credentials are shared with the old
// Console, so that compatibility path additionally requires CLI metadata and
// excludes browser requests. Identity/settings reads never refresh activity.
func ObserveSuccessfulAgentRequest(ctx context.Context, c *app.RequestContext, database *gorm.DB, agentID, startedAt int64, legacy bool) {
	path := c.FullPath()
	if path == "" {
		path = string(c.Path())
	}
	if database == nil || !agentActivityRoute(string(c.Method()), path) || !successfulAgentResponse(c) {
		return
	}
	if legacy && (len(c.GetHeader("X-CLI-Ver")) == 0 || len(c.GetHeader("Origin")) > 0 || len(c.GetHeader("Sec-Fetch-Site")) > 0) {
		return
	}
	header := func(name string) string {
		value := string(c.GetHeader(name))
		limit := 128
		if name == "X-Client-Host" {
			limit = 129
		}
		if len(value) > limit {
			return ""
		}
		return value
	}
	obs := dal.RuntimeObservation{Host: header("X-Client-Host"), Mode: header("X-Client-Mode"), Model: header("X-Client-Model"), CLIVersion: header("X-CLI-Ver"), ObservedAt: startedAt, Active: true}
	if obs.Mode != "plugin" && obs.Mode != "skill" {
		obs.Mode = ""
	}
	observationCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, err := dal.ObserveRuntime(database.WithContext(observationCtx), agentID, obs)
	identity, hasHost := runtimeidentity.Parse(obs.Host)
	// Only parsed product names and enum values are logged, never raw headers,
	// client IDs, request bodies, or credentials. Business failures never enter here.
	fields := []interface{}{"agent_id", agentID, "source", path, "outcome", result.Outcome, "host_present", hasHost, "runtime_name", identity.Name, "mode", obs.Mode, "cli_present", obs.CLIVersion != "", "plugin_version", reqinfo.SafePluginVersion(header("X-Client-Plugin-Version"))}
	if err != nil {
		logger.Ctx(ctx).Warn("agent_runtime_observation", fields...)
		return
	}
	if result.IdentityChanged {
		agentcard.PublishRebuild(ctx, agentID, "runtime_update")
	}
	logger.Ctx(ctx).Info("agent_runtime_observation", fields...)
}
