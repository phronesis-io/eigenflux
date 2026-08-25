package consolev2

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/hertz-contrib/websocket"

	"eigenflux_server/pkg/json"
	"eigenflux_server/pkg/logger"
)

const consoleV2WebSocketProtocol = "eigenflux.console.v2"

type communicationWakeEvent struct {
	Type        string `json:"type"`
	AgentID     string `json:"agent_id"`
	EntityID    string `json:"entity_id,omitempty"`
	ShortID     string `json:"short_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

func validConsoleWebSocketRequest(origin, host, protocol, queryToken, expectedURL string) bool {
	if queryToken != "" || !strings.Contains(","+strings.ReplaceAll(protocol, " ", "")+",", ","+consoleV2WebSocketProtocol+",") {
		return false
	}
	expected, err := url.Parse(expectedURL)
	if err != nil || expected.Scheme == "" || expected.Host == "" || !strings.EqualFold(host, expected.Host) {
		return false
	}
	provided, err := url.Parse(origin)
	if err != nil || provided.User != nil || provided.RawQuery != "" || provided.Fragment != "" ||
		provided.Path != "" || provided.Scheme == "" || provided.Host == "" {
		return false
	}
	return strings.EqualFold(provided.Scheme, expected.Scheme) && strings.EqualFold(provided.Host, expected.Host)
}

func (s *Service) subscribeCommunicationWake(agentID int64) (<-chan communicationWakeEvent, func()) {
	wake := make(chan communicationWakeEvent, 64)
	s.communicationWakeMu.Lock()
	if s.communicationSubs[agentID] == nil {
		s.communicationSubs[agentID] = make(map[chan communicationWakeEvent]struct{})
	}
	s.communicationSubs[agentID][wake] = struct{}{}
	s.communicationWakeMu.Unlock()
	return wake, func() {
		s.communicationWakeMu.Lock()
		delete(s.communicationSubs[agentID], wake)
		if len(s.communicationSubs[agentID]) == 0 {
			delete(s.communicationSubs, agentID)
		}
		close(wake)
		s.communicationWakeMu.Unlock()
	}
}

func (s *Service) notifyCommunicationWake(agentID int64, event communicationWakeEvent) {
	s.communicationWakeMu.RLock()
	defer s.communicationWakeMu.RUnlock()
	for wake := range s.communicationSubs[agentID] {
		select {
		case wake <- event:
		default:
			// A wakeup is not a durable entity. Collapse an overflowing queue to a
			// single REST reconciliation hint instead of growing per-client memory.
			select {
			case <-wake:
			default:
			}
			select {
			case wake <- communicationWakeEvent{Type: "reconcile_required", AgentID: event.AgentID}:
			default:
			}
		}
	}
}

func communicationEvent(agentID int64, payload string) communicationWakeEvent {
	event := communicationWakeEvent{AgentID: strconv.FormatInt(agentID, 10)}
	var responseEvent struct {
		Type            string `json:"type"`
		FriendUID       string `json:"friend_uid"`
		PeerShortID     string `json:"peer_short_id"`
		PeerDisplayName string `json:"peer_display_name"`
	}
	if err := json.Unmarshal([]byte(payload), &responseEvent); err == nil &&
		(responseEvent.Type == "friend_accepted" || responseEvent.Type == "friend_rejected") &&
		responseEvent.FriendUID != "" {
		event.Type = "friends_changed"
		if responseEvent.Type == "friend_rejected" {
			event.Type = "friend_requests_changed"
		}
		event.EntityID = responseEvent.FriendUID
		event.ShortID = responseEvent.PeerShortID
		event.DisplayName = responseEvent.PeerDisplayName
		return event
	}
	switch {
	case payload == "friend_request":
		event.Type = "friend_requests_changed"
	case strings.HasPrefix(payload, "friend_accepted:"):
		event.Type = "friends_changed"
		event.EntityID = strings.TrimPrefix(payload, "friend_accepted:")
	default:
		if id, err := strconv.ParseInt(payload, 10, 64); err == nil && id > 0 {
			event.Type = "private_message_changed"
			event.EntityID = payload
		} else {
			event.Type = "reconcile_required"
		}
	}
	return event
}

func (s *Service) runCommunicationWakeSubscriber() {
	for {
		pubsub := s.redisClient.PSubscribe(context.Background(), "pm:push:*")
		for message := range pubsub.Channel() {
			rawAgentID := strings.TrimPrefix(message.Channel, "pm:push:")
			agentIDValue, err := strconv.ParseInt(rawAgentID, 10, 64)
			if err == nil && agentIDValue > 0 {
				s.notifyCommunicationWake(agentIDValue, communicationEvent(agentIDValue, message.Payload))
			}
		}
		_ = pubsub.Close()
		logger.Default().Warn("Console V2 communication subscriber reconnecting")
		time.Sleep(time.Second)
	}
}

func (s *Service) consoleSessionStillActive(sessionID string, agentID int64) bool {
	var active bool
	now := time.Now().UnixMilli()
	err := s.db.Raw(`SELECT EXISTS(SELECT 1 FROM console_v2_sessions session
		JOIN agent_principals principal ON principal.principal_id = session.principal_id
		WHERE session.session_id = ? AND session.agent_id = ? AND session.status = 'active'
		 AND session.idle_expires_at > ? AND session.absolute_expires_at > ?
		 AND principal.revoked_at IS NULL AND principal.status IN ('limited','active'))`,
		sessionID, agentID, now, now).Scan(&active).Error
	return err == nil && active
}

func (s *Service) streamCommunicationEvents(ctx context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	sessionValue, sessionOK := c.Get("console_session_id")
	sessionID, _ := sessionValue.(string)
	origin := string(c.GetHeader("Origin"))
	host := string(c.Host())
	protocol := string(c.GetHeader("Sec-WebSocket-Protocol"))
	if !sessionOK || sessionID == "" || !validConsoleWebSocketRequest(origin, host, protocol, c.Query("token"), s.publicURL) {
		fail(c, http.StatusForbidden, "WEBSOCKET_ORIGIN_INVALID", "Console V2 WebSocket origin, host, or audience is invalid", nil)
		return
	}
	s.communicationMu.Lock()
	if s.communicationConnections[agentIDValue] >= maxAgentStreams || !s.tryAcquireProcessStream() {
		s.communicationMu.Unlock()
		fail(c, http.StatusTooManyRequests, "COMMUNICATION_CONNECTION_LIMIT", "too many communication streams", nil)
		return
	}
	s.communicationConnections[agentIDValue]++
	s.communicationTotal++
	s.communicationMu.Unlock()
	releaseConnection := func() {
		s.communicationMu.Lock()
		s.communicationConnections[agentIDValue]--
		s.communicationTotal--
		if s.communicationConnections[agentIDValue] <= 0 {
			delete(s.communicationConnections, agentIDValue)
		}
		s.communicationMu.Unlock()
		s.releaseProcessStream()
	}
	upgrader := websocket.HertzUpgrader{
		Subprotocols: []string{consoleV2WebSocketProtocol},
		CheckOrigin: func(request *app.RequestContext) bool {
			return validConsoleWebSocketRequest(string(request.GetHeader("Origin")), string(request.Host()),
				string(request.GetHeader("Sec-WebSocket-Protocol")), request.Query("token"), s.publicURL)
		},
	}
	if err := upgrader.Upgrade(c, func(connection *websocket.Conn) {
		defer releaseConnection()
		defer connection.Close()
		connection.SetReadLimit(8 << 10)
		_ = connection.SetReadDeadline(time.Now().Add(45 * time.Second))
		connection.SetPongHandler(func(string) error {
			return connection.SetReadDeadline(time.Now().Add(45 * time.Second))
		})
		connectionContext, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			defer cancel()
			for {
				if _, _, err := connection.ReadMessage(); err != nil {
					return
				}
			}
		}()
		wake, unsubscribe := s.subscribeCommunicationWake(agentIDValue)
		defer unsubscribe()
		initial := communicationWakeEvent{Type: "reconcile_required", AgentID: strconv.FormatInt(agentIDValue, 10)}
		_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := connection.WriteJSON(initial); err != nil {
			return
		}
		ping := time.NewTicker(30 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-connectionContext.Done():
				return
			case event, ok := <-wake:
				_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if !ok || connection.WriteJSON(event) != nil {
					return
				}
			case <-ping.C:
				if !s.consoleSessionStillActive(sessionID, agentIDValue) {
					_ = connection.WriteControl(websocket.CloseMessage,
						websocket.FormatCloseMessage(4001, "session expired or revoked"), time.Now().Add(time.Second))
					return
				}
				_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if connection.WriteMessage(websocket.PingMessage, nil) != nil {
					return
				}
			}
		}
	}); err != nil {
		releaseConnection()
		logger.Ctx(ctx).Warn("Console V2 WebSocket upgrade failed", "agentID", agentIDValue, "err", fmt.Sprint(err))
	}
}
