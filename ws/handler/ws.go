package handler

import (
	"context"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/hertz-contrib/websocket"

	"eigenflux_server/kitex_gen/eigenflux/auth"
	"eigenflux_server/kitex_gen/eigenflux/auth/authservice"
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
	AuthClient authservice.Client
	PMClient   pmservice.Client
	RDB        *goredis.Client
	Upgrader   *websocket.HertzUpgrader
}

func New(authClient authservice.Client, pmClient pmservice.Client, rdb *goredis.Client) *Handler {
	h := &Handler{
		AuthClient: authClient,
		PMClient:   pmClient,
		RDB:        rdb,
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

	// Validate token via Auth RPC.
	resp, err := h.AuthClient.ValidateSession(ctx, &auth.ValidateSessionReq{
		AccessToken: token,
	})
	if err != nil {
		logger.Ctx(ctx).Error("ws: auth rpc failed", "err", err)
		c.AbortWithMsg("auth service unavailable", 503)
		return
	}
	if resp.BaseResp.Code != 0 {
		c.AbortWithMsg("invalid or expired token", 401)
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
			AgentID: agentID,
			Conn:    ws,
			PMCursor:  cursor,
			Done:    make(chan struct{}),
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
		go push.Run(connCtx, h.RDB, h.PMClient, conn)

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
