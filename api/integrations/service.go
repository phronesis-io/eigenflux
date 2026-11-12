// Package integrations exposes managed enterprise APIs independently of the
// personal CLI authentication/onboarding routes.
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/pkg/integrationfeed"
	"eigenflux_server/pkg/integrationidentity"
	"eigenflux_server/pkg/integrationprofile"
	"eigenflux_server/pkg/logger"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

type Service struct {
	Store       *integrationidentity.Store
	Feed        integrationfeed.Generator
	RateLimiter *RateLimiter
}
type appKey struct{}

const observedAgentKey = "integration_observed_agent"
const failureCodeKey = "integration_failure_code"

func (s *Service) Register(h *server.Hertz) {
	routes := h.Group("/api/v2/integrations", s.limitIngress, s.authenticate, s.limitApplication, s.observe)
	routes.POST("/sdk/installations", s.register)
	routes.POST("/sdk/control:check", s.control)
	routes.PUT("/agents/me/context", s.putContext)
	routes.GET("/agents/me/context", s.getContext)
	routes.GET("/agents/me/card", s.getCard)
	routes.POST("/agents/me/feed", s.pullFeed)
	routes.GET("/agents/me/feed", s.latestFeed)
}

func (s *Service) business(ctx context.Context, c *app.RequestContext) (integrationidentity.Agent, error) {
	owner, _ := ctx.Value(appKey{}).(string)
	installation := string(c.GetHeader("X-EigenFlux-Installation-ID"))
	instance := string(c.GetHeader("X-EigenFlux-Runtime-Instance-ID"))
	if installation == "" || len(installation) > 128 || instance == "" || len(instance) > 128 {
		return integrationidentity.Agent{}, &integrationidentity.Error{Status: 400, Code: "INVALID_INTEGRATION_BINDING"}
	}
	policy, agent, err := s.Store.Check(ctx, owner, installation, instance)
	if agent.AgentID != 0 {
		c.Set(observedAgentKey, agent.AgentID)
	}
	if err != nil {
		return integrationidentity.Agent{}, err
	}
	if !policy.BusinessEnabled {
		return integrationidentity.Agent{}, &integrationidentity.Error{Status: 403, Code: policy.Reason}
	}
	return agent, nil
}

func (s *Service) putContext(ctx context.Context, c *app.RequestContext) {
	bound, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	agent, err := s.business(bound, c)
	if err != nil {
		fail(c, err)
		return
	}
	var input integrationprofile.Input
	if err := body(c, &input); err != nil {
		fail(c, err)
		return
	}
	profiles := integrationprofile.Store{DB: s.Store.DB}
	state, err := profiles.Accept(bound, agent.AgentID, input)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(202, map[string]any{"code": 0, "msg": "accepted", "data": state})
}

func (s *Service) getContext(ctx context.Context, c *app.RequestContext) {
	bound, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	agent, err := s.business(bound, c)
	if err != nil {
		fail(c, err)
		return
	}
	profiles := integrationprofile.Store{DB: s.Store.DB}
	state, err := profiles.Get(bound, agent.AgentID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, map[string]any{"code": 0, "msg": "success", "data": state})
}

func (s *Service) getCard(ctx context.Context, c *app.RequestContext) {
	bound, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	agent, err := s.business(bound, c)
	if err != nil {
		fail(c, err)
		return
	}
	var card struct {
		PublicCard  string
		PrivateCard string
		CardVersion int64
	}
	err = s.Store.DB.WithContext(bound).Table("agent_cards c").Select("c.public_card,c.private_card,c.card_version").Joins("LEFT JOIN integration_matching_profiles m ON m.agent_id=c.agent_id").Where("c.agent_id = ? AND c.source_version >= COALESCE(m.profile_version,0)", agent.AgentID).First(&card).Error
	if err != nil {
		fail(c, &integrationidentity.Error{Status: 503, Code: "INTEGRATION_CARD_NOT_READY"})
		return
	}
	c.JSON(200, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"agent_id": strconv.FormatInt(agent.AgentID, 10), "public_card": json.RawMessage(card.PublicCard), "private_card": json.RawMessage(card.PrivateCard), "card_version": card.CardVersion}})
}

func fail(c *app.RequestContext, err error) {
	var typed *integrationidentity.Error
	if !errors.As(err, &typed) {
		typed = &integrationidentity.Error{Status: 503, Code: "INTEGRATION_UNAVAILABLE"}
	}
	var retry interface{ RetryAfter() int }
	var seconds int
	if errors.As(err, &retry) {
		seconds = retry.RetryAfter()
	}
	writeFailure(c, typed, seconds)
}

func writeFailure(c *app.RequestContext, failure *integrationidentity.Error, seconds int) {
	response := map[string]any{"code": failure.Status, "msg": failure.Code, "error": map[string]any{"code": failure.Code, "message": failure.Code}}
	if seconds > 0 {
		c.Header("Retry-After", strconv.Itoa(seconds))
		response["retry_after_seconds"] = seconds
	}
	c.Set(failureCodeKey, failure.Code)
	c.JSON(failure.Status, response)
}

func (s *Service) authenticate(ctx context.Context, c *app.RequestContext) {
	secret, ok := strings.CutPrefix(string(c.GetHeader("Authorization")), "Bearer ")
	if !ok {
		fail(c, &integrationidentity.Error{Status: 401, Code: "INTEGRATION_AUTH_REQUIRED"})
		c.Abort()
		return
	}
	bound, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	owner, err := s.Store.Authenticate(bound, secret)
	if err != nil {
		fail(c, err)
		c.Abort()
		return
	}
	c.Next(context.WithValue(ctx, appKey{}, owner.AppID))
}

func body(c *app.RequestContext, out any) error {
	raw := c.Request.Body()
	if len(raw) > 16384 {
		return &integrationidentity.Error{Status: 413, Code: "INTEGRATION_REQUEST_TOO_LARGE"}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return &integrationidentity.Error{Status: 400, Code: "INVALID_INTEGRATION_REQUEST"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return &integrationidentity.Error{Status: 400, Code: "INVALID_INTEGRATION_REQUEST"}
	}
	return nil
}

func (s *Service) register(ctx context.Context, c *app.RequestContext) {
	var req integrationidentity.Registration
	if err := body(c, &req); err != nil {
		fail(c, err)
		return
	}
	owner, _ := ctx.Value(appKey{}).(string)
	bound, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := s.Store.Register(bound, owner, req)
	if err != nil {
		fail(c, err)
		return
	}
	c.Set(observedAgentKey, result.Agent.AgentID)
	c.JSON(200, map[string]any{"code": 0, "msg": "success", "data": result})
}

func (s *Service) control(ctx context.Context, c *app.RequestContext) {
	var req struct {
		InstallationID    string `json:"installation_id"`
		RuntimeInstanceID string `json:"runtime_instance_id"`
		SDKState          string `json:"sdk_state,omitempty"`
		SDKFailureCode    string `json:"sdk_failure_code,omitempty"`
	}
	if err := body(c, &req); err != nil {
		fail(c, err)
		return
	}
	if req.InstallationID == "" || len(req.InstallationID) > 128 || req.RuntimeInstanceID == "" || len(req.RuntimeInstanceID) > 128 {
		fail(c, &integrationidentity.Error{Status: 400, Code: "INVALID_INTEGRATION_REQUEST"})
		return
	}
	owner, _ := ctx.Value(appKey{}).(string)
	bound, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	policy, agent, err := s.Store.Check(bound, owner, req.InstallationID, req.RuntimeInstanceID)
	if agent.AgentID != 0 {
		c.Set(observedAgentKey, agent.AgentID)
	}
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.ReportClientState(bound, owner, req.InstallationID, req.RuntimeInstanceID, req.SDKState, req.SDKFailureCode); err != nil {
		logger.Ctx(ctx).Warn("managed SDK diagnostic observation unavailable", "agent_id", agent.AgentID)
	}
	c.JSON(200, map[string]any{"code": 0, "msg": "success", "data": policy})
}

// observe is limited to enterprise routes. Request observations never replace
// the transactional generation counters used for cadence and quotas.
func (s *Service) observe(ctx context.Context, c *app.RequestContext) {
	c.Next(ctx)
	raw, _ := c.Get(observedAgentKey)
	agentID, _ := raw.(int64)
	value, _ := c.Get(failureCodeKey)
	failure, _ := value.(string)
	if failure != "" {
		owner, _ := ctx.Value(appKey{}).(string)
		logger.Ctx(ctx).Warn("managed integration request rejected", "app_id", owner, "agent_id", agentID, "reason", failure)
	}
	if agentID == 0 {
		return
	}
	bound, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := s.Store.RecordRequest(bound, agentID, failure); err != nil {
		logger.Ctx(ctx).Warn("managed request observation unavailable", "agent_id", agentID)
	}
}
