// Package integrations exposes managed enterprise APIs independently of the
// personal CLI authentication/onboarding routes.
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"eigenflux_server/pkg/integrationidentity"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

type Service struct{ Store *integrationidentity.Store }
type appKey struct{}

func (s *Service) Register(h *server.Hertz) {
	h.POST("/api/v2/integrations/sdk/installations", s.authenticate, s.register)
	h.POST("/api/v2/integrations/sdk/control:check", s.authenticate, s.control)
}

func fail(c *app.RequestContext, err error) {
	var typed *integrationidentity.Error
	if !errors.As(err, &typed) {
		typed = &integrationidentity.Error{Status: 503, Code: "INTEGRATION_UNAVAILABLE"}
	}
	c.JSON(typed.Status, map[string]any{"code": typed.Status, "msg": typed.Code, "error": map[string]any{"code": typed.Code, "message": typed.Code}})
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
	c.JSON(200, map[string]any{"code": 0, "msg": "success", "data": result})
}

func (s *Service) control(ctx context.Context, c *app.RequestContext) {
	var req struct {
		InstallationID    string `json:"installation_id"`
		RuntimeInstanceID string `json:"runtime_instance_id"`
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
	policy, _, err := s.Store.Check(bound, owner, req.InstallationID, req.RuntimeInstanceID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, map[string]any{"code": 0, "msg": "success", "data": policy})
}
