package servicereview

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/transport"
	"github.com/phronesis-io/eigenflux-commission/kitex_gen/eigenflux/base"
	api "github.com/phronesis-io/eigenflux-commission/kitex_gen/eigenflux/commission"
	"github.com/phronesis-io/eigenflux-commission/kitex_gen/eigenflux/commission/commissionservice"
)

type Client interface {
	ListModerations(context.Context, *api.ListModerationsReq, ...callopt.Option) (*api.ListModerationsResp, error)
	ResolveModeration(context.Context, *api.ResolveModerationReq, ...callopt.Option) (*api.ResolveModerationResp, error)
}

func NewClient(port int) (Client, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid Commission RPC port")
	}
	return commissionservice.NewClient("CommissionService", client.WithHostPorts(fmt.Sprintf("127.0.0.1:%d", port)),
		client.WithRPCTimeout(10*time.Second), client.WithTransportProtocol(transport.TTHeader), client.WithMetaHandler(transmeta.ClientTTHeaderHandler))
}

func Register(h *server.Hertz, rpc Client) {
	h.GET("/console/api/v1/service-reviews", func(ctx context.Context, c *app.RequestContext) {
		status := c.DefaultQuery("status", "review")
		switch status {
		case "review", "pending", "processing", "pass", "reject", "failed", "stale", "published":
		default:
			respondError(c, 400, "Invalid review status")
			return
		}
		cursor, err := decimalID(c.DefaultQuery("cursor", "0"))
		if err != nil {
			respondError(c, 400, "Invalid cursor")
			return
		}
		limit, err := strconv.ParseInt(c.DefaultQuery("limit", "20"), 10, 32)
		if err != nil || limit < 1 || limit > 100 {
			respondError(c, 400, "Limit must be between 1 and 100")
			return
		}
		response, err := rpc.ListModerations(ctx, &api.ListModerationsReq{Status: status, Cursor: cursor, Limit: int32(limit)})
		if err != nil || response == nil {
			respondError(c, 503, "Review service unavailable")
			return
		}
		respond(c, response.BaseResp, response.ResultJson)
	})
	h.POST("/console/api/v1/service-reviews/:id/resolve", func(ctx context.Context, c *app.RequestContext) {
		id, err := decimalID(c.Param("id"))
		if err != nil || id == 0 {
			respondError(c, 400, "Invalid moderation ID")
			return
		}
		mediaType, _, err := mime.ParseMediaType(string(c.ContentType()))
		if err != nil || mediaType != "application/json" {
			respondError(c, 400, "JSON body required")
			return
		}
		var input struct {
			Decision string `json:"decision"`
			Operator string `json:"operator"`
			Reason   string `json:"reason"`
		}
		if err := json.Unmarshal(c.Request.Body(), &input); err != nil {
			respondError(c, 400, "Invalid review decision")
			return
		}
		input.Operator, input.Reason = strings.TrimSpace(input.Operator), strings.TrimSpace(input.Reason)
		if (input.Decision != "pass" && input.Decision != "reject") || input.Operator == "" || len(input.Operator) > 200 || input.Reason == "" || len(input.Reason) > 8000 {
			respondError(c, 400, "A pass/reject decision, operator (up to 200 bytes), and reason (up to 8000 bytes) are required")
			return
		}
		response, err := rpc.ResolveModeration(ctx, &api.ResolveModerationReq{ModerationId: id, Decision: input.Decision, Reviewer: input.Operator, Reason: input.Reason})
		if err != nil || response == nil {
			respondError(c, 503, "Review service unavailable")
			return
		}
		respond(c, response.BaseResp, response.ResultJson)
	})
}

func decimalID(value string) (int64, error) {
	if value == "" {
		return 0, fmt.Errorf("empty ID")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid ID")
		}
	}
	return strconv.ParseInt(value, 10, 64)
}

func respond(c *app.RequestContext, result *base.BaseResp, payload string) {
	if result == nil {
		respondError(c, 503, "Review service unavailable")
		return
	}
	switch result.Code {
	case 0:
		if !json.Valid([]byte(payload)) {
			respondError(c, 503, "Invalid review service response")
			return
		}
		c.JSON(200, map[string]any{"code": 0, "msg": "success", "data": json.RawMessage(payload)})
	case 400:
		respondError(c, 400, "Invalid review request")
	case 403:
		respondError(c, 403, "Review operation forbidden")
	case 404:
		respondError(c, 404, "Review no longer exists")
	case 409:
		respondError(c, 409, "Review changed or is no longer awaiting a decision. Refresh the queue.")
	default:
		respondError(c, 503, "Review service unavailable")
	}
}

func respondError(c *app.RequestContext, status int, message string) {
	c.JSON(status, map[string]any{"code": status, "msg": message})
}
