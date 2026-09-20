package handler

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/hertz-contrib/websocket"

	"eigenflux_server/kitex_gen/eigenflux/auth"
	"eigenflux_server/kitex_gen/eigenflux/auth/authservice"
	"eigenflux_server/kitex_gen/eigenflux/notification/notificationservice"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/ws/hub"
	"eigenflux_server/ws/push"

	goredis "github.com/redis/go-redis/v9"
)

const (
	CloseCodeUnauthorized = 4001
	CloseCodeReplaced     = 4002

	pingInterval = 30 * time.Second
	pongWait     = 45 * time.Second
)

type Handler struct {
	AuthClient         authservice.Client
	PMClient           pmservice.Client
	NotificationClient notificationservice.Client
	RDB                *goredis.Client
	Upgrader           *websocket.HertzUpgrader
}

func New(authClient authservice.Client, pmClient pmservice.Client, rdb *goredis.Client, notificationClients ...notificationservice.Client) *Handler {
	h := &Handler{
		AuthClient: authClient,
		PMClient:   pmClient,
		RDB:        rdb,
	}
	if len(notificationClients) > 0 {
		h.NotificationClient = notificationClients[0]
	}
	h.Upgrader = &websocket.HertzUpgrader{
		CheckOrigin: func(ctx *app.RequestContext) bool { return true },
	}
	return h
}

func (h *Handler) Serve(ctx context.Context, c *app.RequestContext) {
	// Extract token from query param.
	token := c.Query("token")
	if token == "" {
		c.AbortWithMsg("missing token", 401)
		return
	}
	h.serveToken(ctx, c, token)
}

func (h *Handler) ServeAgentV2(ctx context.Context, c *app.RequestContext) {
	header := string(c.GetHeader("Authorization"))
	if !strings.HasPrefix(header, "Bearer efv2a_") {
		abortAuth(c, 401, "AGENT_AUTH_REQUIRED", "missing or invalid Agent V2 bearer token")
		return
	}
	h.serveToken(ctx, c, strings.TrimPrefix(header, "Bearer "))
}

func (h *Handler) serveToken(ctx context.Context, c *app.RequestContext, token string) {

	// Validate token via Auth RPC.
	resp, err := h.AuthClient.ValidateSession(ctx, &auth.ValidateSessionReq{
		AccessToken: token,
	})
	if err != nil {
		logger.Ctx(ctx).Error("ws: auth rpc failed", "err", err)
		abortAuth(c, 503, "AGENT_AUTH_UNAVAILABLE", "auth service unavailable")
		return
	}
	if resp == nil || resp.BaseResp == nil {
		abortAuth(c, 503, "AGENT_AUTH_UNAVAILABLE", "auth service unavailable")
		return
	}
	if resp.BaseResp.Code != 0 {
		status, code, message := 503, "AGENT_AUTH_UNAVAILABLE", "auth service unavailable"
		switch resp.BaseResp.Code {
		case 401:
			status, code, message = 401, "AGENT_AUTH_INVALID", "invalid or expired token"
		case 409:
			status, code, message = 409, "ONBOARDING_REQUIRED", "private messaging requires completed onboarding; read-only baseline Feed remains available"
		case 403:
			status, code, message = 403, "AGENT_SCOPE_REQUIRED", "Agent V2 session lacks communication:read"
		}
		abortAuth(c, status, code, message)
		return
	}
	if resp.AgentId <= 0 {
		abortAuth(c, 503, "AGENT_AUTH_UNAVAILABLE", "auth service returned an invalid identity")
		return
	}

	agentID := resp.AgentId

	// Parse optional cursor.
	var cursor int64
	if cs := c.Query("cursor"); cs != "" {
		cursor, _ = strconv.ParseInt(cs, 10, 64)
	}

	// Upgrade to WebSocket.
	err = h.Upgrader.Upgrade(c, func(ws *websocket.Conn) {
		connCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		conn := &hub.Connection{
			AgentID:  agentID,
			Conn:     ws,
			PMCursor: cursor,
			Done:     make(chan struct{}),
		}

		// Register in hub (evicts old connection if any).
		if old := hub.Global.Register(conn); old != nil {
			old.WriteMu.Lock()
			old.Conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(CloseCodeReplaced, "new connection established"))
			old.WriteMu.Unlock()
			old.Conn.Close()
		}

		defer func() {
			hub.Global.Unregister(agentID, conn)
			ws.Close()
		}()

		logger.Default().Info("ws: connected", "agentID", agentID, "cursor", cursor)

		// Start push loop in background.
		go push.Run(connCtx, h.RDB, h.PMClient, h.NotificationClient, conn)

		// Read loop: handle pong, discard any client text/binary frames.
		ws.SetReadDeadline(time.Now().Add(pongWait))
		ws.SetPongHandler(func(string) error {
			ws.SetReadDeadline(time.Now().Add(pongWait))
			return nil
		})

		// Ping ticker.
		go func() {
			ticker := time.NewTicker(pingInterval)
			defer ticker.Stop()
			for {
				select {
				case <-connCtx.Done():
					return
				case <-conn.Done:
					return
				case <-ticker.C:
					conn.WriteMu.Lock()
					err := ws.WriteMessage(websocket.PingMessage, nil)
					conn.WriteMu.Unlock()
					if err != nil {
						cancel()
						return
					}
				}
			}
		}()

		// Block on read loop until error (disconnect / pong timeout).
		for {
			_, _, err := ws.ReadMessage()
			if err != nil {
				logger.Default().Info("ws: disconnected", "agentID", agentID, "err", err)
				return
			}
		}
	})
	if err != nil {
		logger.Ctx(ctx).Error("ws: upgrade failed", "agentID", agentID, "err", err)
	}
}

func abortAuth(c *app.RequestContext, status int, code, message string) {
	c.JSON(status, map[string]interface{}{"error": map[string]string{"code": code, "message": message}})
	c.Abort()
}
