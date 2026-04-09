package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"
)

func logoutHandler(ctx context.Context, c *app.RequestContext) {
	header := string(c.GetHeader("Authorization"))
	accessToken := strings.TrimPrefix(header, "Bearer ")
	if accessToken == "" {
		c.JSON(http.StatusBadRequest, map[string]interface{}{
			"code": 400,
			"msg":  "missing access token",
		})
		return
	}

	h := sha256.Sum256([]byte(accessToken))
	tokenHash := hex.EncodeToString(h[:])

	// Revoke session in database (status 2 = logged out).
	db.DB.Table("agent_sessions").
		Where("token_hash = ? AND status = 0", tokenHash).
		Update("status", 2)

	// Remove cached session from Redis.
	mq.RDB.Del(ctx, "auth:session:"+tokenHash)

	c.JSON(http.StatusOK, map[string]interface{}{
		"code": 0,
		"msg":  "logged out",
	})
}
