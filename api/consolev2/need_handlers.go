package consolev2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/need"
)

func (s *Service) registerNeeds(h *server.Hertz) {
	h.POST("/api/v2/need-inputs", s.agentAuth("context:write"), s.requireCompleted, s.createNeedInput)
	h.GET("/api/v2/need-inputs", s.agentAuth("context:read"), s.requireCompleted, s.listNeedInputs)
	h.GET("/api/v2/need-inputs/:need_input_id", s.agentAuth("context:read"), s.requireCompleted, s.getNeedInput)
}
func (s *Service) needStore() need.Store { return need.Store{DB: s.db, IDs: s.idgen} }
func needReply(c *app.RequestContext, status int, data any) {
	c.Header("Cache-Control", "private, no-store")
	c.JSON(status, map[string]any{"code": 0, "msg": "success", "data": data})
}
func needFailure(ctx context.Context, c *app.RequestContext, err error) {
	var field *need.FieldError
	switch {
	case errors.As(err, &field):
		fail(c, 400, "INVALID_NEED_INPUT", "invalid NeedInput", map[string]any{"errors": []*need.FieldError{field}})
	case errors.Is(err, need.ErrNotFound):
		fail(c, 404, "NEED_INPUT_NOT_FOUND", "NeedInput not found", nil)
	case errors.Is(err, need.ErrConflict):
		fail(c, 409, "IDEMPOTENCY_CONFLICT", "key was used with different input", nil)
	case errors.Is(err, need.ErrStaleIntent):
		fail(c, 409, "INTENT_REVISION_STALE", "intent is no longer the submitted version", nil)
	default:
		logger.Ctx(ctx).Error("need_input_operation_failed")
		fail(c, 500, "NEED_INPUT_STORAGE_FAILED", "could not persist or read the NeedInput", nil)
	}
}
func needInputID(c *app.RequestContext) (int64, error) {
	raw := c.Param("need_input_id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, &need.FieldError{Path: "need_input_id", Reason: "invalid_id"}
	}
	return id, nil
}
func (s *Service) createNeedInput(ctx context.Context, c *app.RequestContext) {
	owner, _ := agentID(c)
	raw, err := c.Body()
	if err != nil {
		fail(c, 400, "INVALID_NEED_INPUT", "could not read body", nil)
		return
	}
	if len(raw) > need.MaxBodyBytes {
		fail(c, 413, "NEED_INPUT_TOO_LARGE", "NeedInput body exceeds 32 KiB", nil)
		return
	}
	r, replayed, err := s.needStore().Create(ctx, owner, string(c.GetHeader("Idempotency-Key")), raw, time.Now().UnixMilli())
	if err != nil {
		needFailure(ctx, c, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	needReply(c, status, map[string]any{"need_input": r, "replayed": replayed})
}
func (s *Service) getNeedInput(ctx context.Context, c *app.RequestContext) {
	owner, _ := agentID(c)
	id, err := needInputID(c)
	if err != nil {
		needFailure(ctx, c, err)
		return
	}
	r, err := s.needStore().Get(ctx, owner, id)
	if err != nil {
		needFailure(ctx, c, err)
		return
	}
	needReply(c, 200, map[string]any{"need_input": r})
}
func (s *Service) listNeedInputs(ctx context.Context, c *app.RequestContext) {
	owner, _ := agentID(c)
	cursor := int64(0)
	limit := int64(20)
	var err error
	if raw := c.Query("cursor"); raw != "" {
		cursor, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor <= 0 {
			needFailure(ctx, c, &need.FieldError{Path: "cursor", Reason: "invalid_id"})
			return
		}
	}
	if raw := c.Query("limit"); raw != "" {
		limit, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			needFailure(ctx, c, &need.FieldError{Path: "limit", Reason: "invalid_limit"})
			return
		}
	}
	page, err := s.needStore().List(ctx, owner, cursor, limit)
	if err != nil {
		needFailure(ctx, c, err)
		return
	}
	needReply(c, 200, page)
}
