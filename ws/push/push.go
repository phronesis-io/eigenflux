package push

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/hertz-contrib/websocket"
	"github.com/redis/go-redis/v9"

	"eigenflux_server/kitex_gen/eigenflux/pm"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/ws/hub"
)

// Message is the envelope pushed to the client over WebSocket.
type Message struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// PMFetchData mirrors the FetchPMResp.data shape sent via the REST API.
type PMFetchData struct {
	Messages   []PMMessageData `json:"messages"`
	NextCursor string          `json:"next_cursor"`
}

type PMMessageData struct {
	MsgID        string `json:"msg_id"`
	ConvID       string `json:"conv_id"`
	SenderID     string `json:"sender_id"`
	ReceiverID   string `json:"receiver_id"`
	Content      string `json:"content"`
	IsRead       bool   `json:"is_read"`
	CreatedAt    int64  `json:"created_at"`
	SenderName   string `json:"sender_name,omitempty"`
	ReceiverName string `json:"receiver_name,omitempty"`
}

// Run is the main push loop for a single connection. It blocks until the
// connection's Done channel is closed or the context is cancelled.
func Run(ctx context.Context, rdb *redis.Client, pmClient pmservice.Client, conn *hub.Connection) {
	channel := fmt.Sprintf("pm:push:%d", conn.AgentID)
	pubsub := rdb.Subscribe(ctx, channel)
	defer pubsub.Close()

	// Initial fetch on connect.
	fetchAndPush(ctx, pmClient, conn)

	ch := pubsub.Channel()
	for {
		select {
		case <-conn.Done:
			return
		case <-ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			fetchAndPush(ctx, pmClient, conn)
		}
	}
}

// writeMu protects concurrent writes to the same websocket conn.
var (
	writeMuMap sync.Map // *hub.Connection -> *sync.Mutex
)

func GetWriteMu(conn *hub.Connection) *sync.Mutex {
	val, _ := writeMuMap.LoadOrStore(conn, &sync.Mutex{})
	return val.(*sync.Mutex)
}

func cleanWriteMu(conn *hub.Connection) {
	writeMuMap.Delete(conn)
}

func fetchAndPush(ctx context.Context, pmClient pmservice.Client, conn *hub.Connection) {
	resp, err := pmClient.FetchPM(ctx, &pm.FetchPMReq{
		AgentId: conn.AgentID,
		Cursor:  &conn.PMCursor,
	})
	if err != nil {
		logger.Ctx(ctx).Error("ws: FetchPM failed", "agentID", conn.AgentID, "err", err)
		return
	}
	if resp.BaseResp.Code != 0 {
		logger.Ctx(ctx).Error("ws: FetchPM error", "agentID", conn.AgentID, "code", resp.BaseResp.Code, "msg", resp.BaseResp.Msg)
		return
	}
	if len(resp.Messages) == 0 {
		return
	}

	// Build push payload.
	msgs := make([]PMMessageData, len(resp.Messages))
	for i, m := range resp.Messages {
		msgs[i] = PMMessageData{
			MsgID:      fmt.Sprintf("%d", m.MsgId),
			ConvID:     fmt.Sprintf("%d", m.ConvId),
			SenderID:   fmt.Sprintf("%d", m.SenderId),
			ReceiverID: fmt.Sprintf("%d", m.ReceiverId),
			Content:    m.Content,
			IsRead:     m.IsRead,
			CreatedAt:  m.CreatedAt,
		}
		if m.SenderName != nil {
			msgs[i].SenderName = *m.SenderName
		}
		if m.ReceiverName != nil {
			msgs[i].ReceiverName = *m.ReceiverName
		}
	}

	data := PMFetchData{
		Messages:   msgs,
		NextCursor: fmt.Sprintf("%d", resp.NextCursor),
	}
	envelope := Message{Type: "pm_fetch", Data: data}

	payload, err := json.Marshal(envelope)
	if err != nil {
		logger.Ctx(ctx).Error("ws: marshal failed", "err", err)
		return
	}

	mu := GetWriteMu(conn)
	mu.Lock()
	err = conn.Conn.WriteMessage(websocket.TextMessage, payload)
	mu.Unlock()
	if err != nil {
		logger.Ctx(ctx).Error("ws: write failed", "agentID", conn.AgentID, "err", err)
		return
	}

	// Advance cursor.
	conn.PMCursor = resp.NextCursor
	logger.Ctx(ctx).Info("ws: pushed messages", "agentID", conn.AgentID, "count", len(resp.Messages), "cursor", conn.PMCursor)
}

// CleanupConn removes the write mutex for a connection. Call on disconnect.
func CleanupConn(conn *hub.Connection) {
	cleanWriteMu(conn)
}
