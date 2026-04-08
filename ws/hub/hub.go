package hub

import (
	"sync"

	"github.com/hertz-contrib/websocket"
)

type Connection struct {
	AgentID int64
	Conn    *websocket.Conn
	PMCursor  int64
	Done    chan struct{} // closed when this connection should shut down
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
