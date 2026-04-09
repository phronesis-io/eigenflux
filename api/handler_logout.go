package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
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
	if result := db.DB.Table("agent_sessions").
		Where("token_hash = ? AND status = 0", tokenHash).
		Update("status", 2); result.Error != nil {
		log.Printf("logout: db update failed: %v", result.Error)
	}

	// Remove cached session from Redis.
	if err := mq.RDB.Del(ctx, "auth:session:"+tokenHash).Err(); err != nil {
		log.Printf("logout: redis del failed: %v", err)
	}

	c.JSON(http.StatusOK, map[string]interface{}{
		"code": 0,
		"msg":  "logged out",
	})
}
