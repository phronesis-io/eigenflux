package hub

import (
	"errors"
	"sync"
	"time"

	"github.com/hertz-contrib/websocket"
)

type Connection struct {
	SessionActive func() bool
	AgentID       int64
	Conn          *websocket.Conn
	PMCursor      int64         // written only by the push goroutine (push.Run); no external lock
	Done          chan struct{} // closed when this connection should shut down
	WriteMu       sync.Mutex    // protects concurrent writes to Conn
}

// WriteAuthenticated rechecks long-lived connections before every outgoing
// payload or heartbeat. Closing the socket also unblocks the reader.
func (c *Connection) WriteAuthenticated(kind int, payload []byte) error {
	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()
	if !c.SessionActive() {
		_ = c.Conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "session unavailable"), time.Now().Add(time.Second))
		_ = c.Conn.Close()
		return errors.New("websocket session unavailable")
	}
	return c.Conn.WriteMessage(kind, payload)
}

type Hub struct {
	mu    sync.RWMutex
	conns map[int64]*Connection
}

var Global = &Hub{
	conns: make(map[int64]*Connection),
}

// Register adds a connection. If the agent already has one, the old connection
// is evicted: its Done channel is closed and the caller is responsible for
// sending the close frame on the old conn.
// Returns the evicted connection (nil if none).
func (h *Hub) Register(c *Connection) *Connection {
	h.mu.Lock()
	defer h.mu.Unlock()
	old := h.conns[c.AgentID]
	if old != nil {
		close(old.Done)
	}
	h.conns[c.AgentID] = c
	return old
}

func (h *Hub) Unregister(agentID int64, c *Connection) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[agentID] == c {
		delete(h.conns, agentID)
	}
}

func (h *Hub) Get(agentID int64) *Connection {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[agentID]
}

func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}
