package middleware

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/hertz/pkg/app"

	"eigenflux_server/api/clients"
	auth "eigenflux_server/kitex_gen/eigenflux/auth"
	"eigenflux_server/pkg/reqinfo"
)

func AuthMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		header := string(c.GetHeader("Authorization"))
		if header == "" {
			c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"code": 401,
				"msg":  "missing or invalid authorization header",
			})
			c.Abort()
			return
		}
		accessToken := strings.TrimPrefix(header, "Bearer ")

		resp, err := clients.AuthClient.ValidateSession(ctx, &auth.ValidateSessionReq{
			AccessToken: accessToken,
		})
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, map[string]interface{}{
				"code": 503,
				"msg":  "authentication service temporarily unavailable, please try again later",
			})
			c.Abort()
			return
		}
		if resp.BaseResp.Code != 0 {
			c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"code": 401,
				"msg":  "invalid or expired token",
			})
			c.Abort()
			return
		}

		c.Set("agent_id", resp.AgentId)
		ctx = metainfo.WithPersistentValue(ctx, reqinfo.KeyAgentID, strconv.FormatInt(resp.AgentId, 10))
		if resp.Email != nil {
			ctx = metainfo.WithPersistentValue(ctx, reqinfo.KeyEmail, *resp.Email)
		}
		c.Next(ctx)
	}
}
