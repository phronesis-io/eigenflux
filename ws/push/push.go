package push

import (
	"context"
	"encoding/json"
	"fmt"

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

// PMFetchData mirrors the FetchPMResp.data shape sent via the REST API,
// extended on the WS initial envelope with history + pending friend requests.
type PMFetchData struct {
	Messages            []PMMessageData     `json:"messages"`
	NextCursor          string              `json:"next_cursor"`
	HistoryMessages     []PMMessageData     `json:"history_messages,omitempty"`
	FriendRequests        []FriendRequestData `json:"friend_requests,omitempty"`
	FriendRequestsHasMore bool                `json:"friend_requests_has_more,omitempty"`
}

type FriendRequestData struct {
	RequestID string `json:"request_id"`
	FromUID   string `json:"from_uid"`
	ToUID     string `json:"to_uid"`
	CreatedAt int64  `json:"created_at"`
	FromName  string `json:"from_name,omitempty"`
	ToName    string `json:"to_name,omitempty"`
	Greeting  string `json:"greeting,omitempty"`
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

func buildPMMessages(msgs []*pm.PMMessage) []PMMessageData {
	result := make([]PMMessageData, len(msgs))
	for i, m := range msgs {
		result[i] = PMMessageData{
			MsgID:      fmt.Sprintf("%d", m.MsgId),
			ConvID:     fmt.Sprintf("%d", m.ConvId),
			SenderID:   fmt.Sprintf("%d", m.SenderId),
			ReceiverID: fmt.Sprintf("%d", m.ReceiverId),
			Content:    m.Content,
			IsRead:     m.IsRead,
			CreatedAt:  m.CreatedAt,
		}
		if m.SenderName != nil {
			result[i].SenderName = *m.SenderName
		}
		if m.ReceiverName != nil {
			result[i].ReceiverName = *m.ReceiverName
		}
	}
	return result
}

func buildFriendRequests(infos []*pm.FriendRequestInfo) []FriendRequestData {
	result := make([]FriendRequestData, len(infos))
	for i, fr := range infos {
		result[i] = FriendRequestData{
			RequestID: fmt.Sprintf("%d", fr.RequestId),
			FromUID:   fmt.Sprintf("%d", fr.FromUid),
			ToUID:     fmt.Sprintf("%d", fr.ToUid),
			CreatedAt: fr.CreatedAt,
		}
		if fr.FromName != nil {
			result[i].FromName = *fr.FromName
		}
		if fr.ToName != nil {
			result[i].ToName = *fr.ToName
		}
		if fr.Greeting != nil {
			result[i].Greeting = *fr.Greeting
		}
	}
	return result
}

const pendingFRDirection = "incoming"
const pendingFRLimit = int32(5)

// fetchPendingFriendRequests fetches incoming pending friend requests for the agent.
// Returns nil slices on error (logged as warning).
func fetchPendingFriendRequests(ctx context.Context, pmClient pmservice.Client, agentID int64) ([]FriendRequestData, bool) {
	limit := pendingFRLimit
	resp, err := pmClient.ListFriendRequests(ctx, &pm.ListFriendRequestsReq{
		AgentId:   agentID,
		Direction: pendingFRDirection,
		Limit:     &limit,
	})
	if err != nil {
		logger.Ctx(ctx).Warn("ws: ListFriendRequests failed", "agentID", agentID, "err", err)
		return nil, false
	}
	if resp == nil || resp.BaseResp == nil {
		logger.Ctx(ctx).Warn("ws: ListFriendRequests returned nil response or base_resp", "agentID", agentID)
		return nil, false
	}
	if resp.BaseResp.Code != 0 {
		logger.Ctx(ctx).Warn("ws: ListFriendRequests error", "agentID", agentID, "code", resp.BaseResp.Code, "msg", resp.BaseResp.Msg)
		return nil, false
	}
	pending := buildFriendRequests(resp.Requests)
	hasMore := resp.HasMore != nil && *resp.HasMore
	return pending, hasMore
}

// Run is the main push loop for a single connection. It blocks until the
// connection's Done channel is closed or the context is cancelled.
func Run(ctx context.Context, rdb *redis.Client, pmClient pmservice.Client, conn *hub.Connection) {
	channel := fmt.Sprintf("pm:push:%d", conn.AgentID)
	pubsub := rdb.Subscribe(ctx, channel)
	defer pubsub.Close()

	// Initial fetch on connect.
	pushInitial(ctx, pmClient, conn)

	ch := pubsub.Channel()
	for {
		select {
		case <-conn.Done:
			return
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fetchAndPush(ctx, pmClient, conn, msg.Payload)
		}
	}
}

func pushInitial(ctx context.Context, pmClient pmservice.Client, conn *hub.Connection) {
	// Only fetch history on first connect (cursor=0). On reconnect (cursor>0)
	// the client already has history — sending it again causes duplicate display.
	var history []PMMessageData
	if conn.PMCursor == 0 {
		histResp, err := pmClient.FetchPMHistory(ctx, &pm.FetchPMHistoryReq{AgentId: conn.AgentID})
		if err != nil {
			logger.Ctx(ctx).Error("ws: FetchPMHistory failed", "agentID", conn.AgentID, "err", err)
		} else if histResp == nil || histResp.BaseResp == nil {
			logger.Ctx(ctx).Error("ws: FetchPMHistory returned nil response or base_resp", "agentID", conn.AgentID)
		} else if histResp.BaseResp.Code != 0 {
			logger.Ctx(ctx).Error("ws: FetchPMHistory error", "agentID", conn.AgentID, "code", histResp.BaseResp.Code, "msg", histResp.BaseResp.Msg)
		} else {
			history = buildPMMessages(histResp.Messages)
		}
	}

	pending, pendingHasMore := fetchPendingFriendRequests(ctx, pmClient, conn.AgentID)

	unreadResp, err := pmClient.FetchPM(ctx, &pm.FetchPMReq{
		AgentId: conn.AgentID,
		Cursor:  &conn.PMCursor,
	})
	var unread []PMMessageData
	nextCursor := conn.PMCursor
	if err != nil {
		logger.Ctx(ctx).Error("ws: FetchPM failed", "agentID", conn.AgentID, "err", err)
	} else if unreadResp == nil || unreadResp.BaseResp == nil {
		logger.Ctx(ctx).Error("ws: FetchPM returned nil response or base_resp", "agentID", conn.AgentID)
	} else if unreadResp.BaseResp.Code != 0 {
		logger.Ctx(ctx).Error("ws: FetchPM error", "agentID", conn.AgentID, "code", unreadResp.BaseResp.Code, "msg", unreadResp.BaseResp.Msg)
	} else {
		unread = buildPMMessages(unreadResp.Messages)
		nextCursor = unreadResp.NextCursor
	}

	if len(unread) == 0 && len(pending) == 0 {
		return
	}

	data := PMFetchData{
		Messages:            unread,
		NextCursor:          fmt.Sprintf("%d", nextCursor),
		HistoryMessages:     history,
		FriendRequests:        pending,
		FriendRequestsHasMore: pendingHasMore,
	}
	envelope := Message{Type: "pm_push", Data: data}
	payload, err := json.Marshal(envelope)
	if err != nil {
		logger.Ctx(ctx).Error("ws: marshal initial failed", "err", err)
		return
	}

	conn.WriteMu.Lock()
	err = conn.Conn.WriteMessage(websocket.TextMessage, payload)
	conn.WriteMu.Unlock()
	if err != nil {
		logger.Ctx(ctx).Error("ws: write initial failed", "agentID", conn.AgentID, "err", err)
		return
	}

	conn.PMCursor = nextCursor
	logger.Ctx(ctx).Info("ws: pushed initial",
		"agentID", conn.AgentID,
		"unread", len(unread),
		"history", len(history),
		"pending_shown", len(pending),
		"pending_has_more", pendingHasMore,
		"cursor", conn.PMCursor)
}

func fetchAndPush(ctx context.Context, pmClient pmservice.Client, conn *hub.Connection, eventPayload string) {
	isFriendRequestEvent := eventPayload == "friend_request"

	// Always fetch unread PMs.
	resp, err := pmClient.FetchPM(ctx, &pm.FetchPMReq{
		AgentId: conn.AgentID,
		Cursor:  &conn.PMCursor,
	})
	if err != nil {
		logger.Ctx(ctx).Error("ws: FetchPM failed", "agentID", conn.AgentID, "err", err)
		return
	}
	if resp == nil || resp.BaseResp == nil {
		logger.Ctx(ctx).Error("ws: FetchPM returned nil response or base_resp", "agentID", conn.AgentID)
		return
	}
	if resp.BaseResp.Code != 0 {
		logger.Ctx(ctx).Error("ws: FetchPM error", "agentID", conn.AgentID, "code", resp.BaseResp.Code, "msg", resp.BaseResp.Msg)
		return
	}

	msgs := buildPMMessages(resp.Messages)

	// Only fetch friend requests when the event is a friend_request,
	// not on every PM event — avoids unnecessary RPC/DB calls at scale.
	var pending []FriendRequestData
	var pendingHasMore bool
	if isFriendRequestEvent {
		pending, pendingHasMore = fetchPendingFriendRequests(ctx, pmClient, conn.AgentID)
	}

	if len(msgs) == 0 && len(pending) == 0 {
		return
	}

	data := PMFetchData{
		Messages:              msgs,
		NextCursor:            fmt.Sprintf("%d", resp.NextCursor),
		FriendRequests:        pending,
		FriendRequestsHasMore: pendingHasMore,
	}
	envelope := Message{Type: "pm_push", Data: data}

	payload, err := json.Marshal(envelope)
	if err != nil {
		logger.Ctx(ctx).Error("ws: marshal failed", "err", err)
		return
	}

	conn.WriteMu.Lock()
	err = conn.Conn.WriteMessage(websocket.TextMessage, payload)
	conn.WriteMu.Unlock()
	if err != nil {
		logger.Ctx(ctx).Error("ws: write failed", "agentID", conn.AgentID, "err", err)
		return
	}

	if len(resp.Messages) > 0 {
		conn.PMCursor = resp.NextCursor
	}
	logger.Ctx(ctx).Info("ws: pushed", "agentID", conn.AgentID, "event", eventPayload, "msgCount", len(msgs), "frCount", len(pending), "cursor", conn.PMCursor)
}
