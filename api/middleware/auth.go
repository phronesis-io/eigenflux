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

// blockedAgentEmails holds normalized (lowercased) agent emails that are denied
// at the auth gate. Populated once at startup via SetBlockedAgentEmails and
// treated as read-only afterwards, so concurrent request reads need no lock.
var blockedAgentEmails map[string]struct{}

// SetBlockedAgentEmails installs the deny-list of agent emails. Call once at
// startup, before the server starts serving requests. Emails are matched
// case-insensitively; blank entries are ignored.
func SetBlockedAgentEmails(emails []string) {
	m := make(map[string]struct{}, len(emails))
	for _, e := range emails {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			m[e] = struct{}{}
		}
	}
	blockedAgentEmails = m
}

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

		// Deny-listed accounts (e.g. spam ingestion identities) are rejected here,
		// blocking every authenticated route including broadcast publish. Keyed by
		// email so a re-login keeps the block even if the agent row changes.
		if resp.Email != nil && len(blockedAgentEmails) > 0 {
			if _, blocked := blockedAgentEmails[strings.ToLower(*resp.Email)]; blocked {
				c.JSON(http.StatusForbidden, map[string]interface{}{
					"code": 403,
					"msg":  "account suspended",
				})
				c.Abort()
				return
			}
		}

		c.Set("agent_id", resp.AgentId)
		ctx = metainfo.WithPersistentValue(ctx, reqinfo.KeyAgentID, strconv.FormatInt(resp.AgentId, 10))
		if resp.Email != nil {
			ctx = metainfo.WithPersistentValue(ctx, reqinfo.KeyEmail, *resp.Email)
		}
		c.Next(ctx)
	}
}
