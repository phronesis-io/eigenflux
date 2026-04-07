# WebSocket Real-Time PM Push Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a standalone WebSocket service that pushes new private messages to connected clients in real time via Redis Pub/Sub.

**Architecture:** A new top-level `ws/` Hertz service listens on port 8088. On WS upgrade, it validates the auth token via Auth RPC, subscribes to Redis Pub/Sub channel `pm:push:{agentID}`, and calls PM RPC `FetchPM` to push messages. The PM service publishes a notification after each successful `SendPM`.

**Tech Stack:** Go, Hertz, hertz-contrib/websocket, Redis Pub/Sub, Kitex RPC clients (Auth, PM)

---

### Task 1: Add `WSPort` to config and update build/start scripts

**Files:**
- Modify: `pkg/config/config.go:20-149`
- Modify: `scripts/common/build.sh:21-32`
- Modify: `scripts/local/start_local.sh:24-31,167-178`

- [ ] **Step 1: Add WSPort field to Config struct**

In `pkg/config/config.go`, add the field after `NotificationRPCPort`:

```go
NotificationRPCPort     int
WSPort                  int
```

- [ ] **Step 2: Add WSPort initialization in Load()**

In the `Load()` function, after the `NotificationRPCPort` line (line 117):

```go
NotificationRPCPort:     getEnvInt("NOTIFICATION_RPC_PORT", 8887),
WSPort:                  getEnvInt("WS_PORT", 8088),
```

- [ ] **Step 3: Add ws to build.sh ALL_SERVICES**

In `scripts/common/build.sh`, add after the `"cron:./pipeline/cron/"` entry (line 31):

```bash
ALL_SERVICES=(
  "profile:./rpc/profile/"
  "item:./rpc/item/"
  "sort:./rpc/sort/"
  "feed:./rpc/feed/"
  "pm:./rpc/pm/"
  "auth:./rpc/auth/"
  "notification:./rpc/notification/"
  "api:./api/"
  "pipeline:./pipeline/"
  "cron:./pipeline/cron/"
  "ws:./ws/"
)
```

- [ ] **Step 4: Add WS_PORT to start_local.sh**

Add the port variable after the `NOTIFICATION_RPC_PORT` line (line 31):

```bash
NOTIFICATION_RPC_PORT="${NOTIFICATION_RPC_PORT:-8887}"
WS_PORT="${WS_PORT:-8088}"
```

Add to SERVICE_MAP after `"notification:${NOTIFICATION_RPC_PORT}"`:

```bash
SERVICE_MAP=(
  "profile:${PROFILE_RPC_PORT}"
  "item:${ITEM_RPC_PORT}"
  "sort:${SORT_RPC_PORT}"
  "feed:${FEED_RPC_PORT}"
  "pm:${PM_RPC_PORT}"
  "auth:${AUTH_RPC_PORT}"
  "notification:${NOTIFICATION_RPC_PORT}"
  "ws:${WS_PORT}"
  "api:${API_PORT}"
  "pipeline:"
  "cron:"
)
```

- [ ] **Step 5: Verify build compiles existing services**

Run: `bash scripts/common/build.sh api pm`
Expected: Both compile OK (ws will fail since it doesn't exist yet, that's fine)

- [ ] **Step 6: Commit**

```bash
git add pkg/config/config.go scripts/common/build.sh scripts/local/start_local.sh
git commit -m "feat(ws): add WSPort config and register ws in build/start scripts"
```

---

### Task 2: Create Hub — connection management

**Files:**
- Create: `ws/hub/hub.go`

- [ ] **Step 1: Create ws/hub directory**

```bash
mkdir -p ws/hub
```

- [ ] **Step 2: Write hub.go**

Create `ws/hub/hub.go`:

```go
package hub

import (
	"sync"

	"github.com/hertz-contrib/websocket"
)

type Connection struct {
	AgentID int64
	Conn    *websocket.Conn
	Cursor  int64
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
	// Only remove if the stored connection is the same one (avoid race with replacement).
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
```

- [ ] **Step 3: Verify it compiles**

```bash
cd ws && go build ./hub/ && cd ..
```

Note: This will fail until go.mod has the websocket dependency. We'll add it in Task 4. For now, just create the file.

- [ ] **Step 4: Commit**

```bash
git add ws/hub/hub.go
git commit -m "feat(ws): add Hub connection manager"
```

---

### Task 3: Create push loop — Redis subscribe + FetchPM + push

**Files:**
- Create: `ws/push/push.go`

- [ ] **Step 1: Create ws/push directory**

```bash
mkdir -p ws/push
```

- [ ] **Step 2: Write push.go**

Create `ws/push/push.go`:

```go
package push

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

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
// Key is the Connection pointer address — we only need one lock per conn.
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
		Cursor:  &conn.Cursor,
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
	conn.Cursor = resp.NextCursor
	logger.Ctx(ctx).Info("ws: pushed messages", "agentID", conn.AgentID, "count", len(resp.Messages), "cursor", conn.Cursor)
}

// CleanupConn removes the write mutex for a connection. Call on disconnect.
func CleanupConn(conn *hub.Connection) {
	cleanWriteMu(conn)
}
```

- [ ] **Step 3: Commit**

```bash
git add ws/push/push.go
git commit -m "feat(ws): add push loop with Redis Pub/Sub and FetchPM"
```

---

### Task 4: Create WebSocket handler — auth + upgrade

**Files:**
- Create: `ws/handler/ws.go`

- [ ] **Step 1: Create ws/handler directory**

```bash
mkdir -p ws/handler
```

- [ ] **Step 2: Write ws.go**

Create `ws/handler/ws.go`:

```go
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
			Cursor:  cursor,
			Done:    make(chan struct{}),
		}

		// Register in hub (evicts old connection if any).
		if old := hub.Global.Register(conn); old != nil {
			old.Conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(CloseCodeReplaced, "new connection established"))
			old.Conn.Close()
		}

		defer func() {
			hub.Global.Unregister(agentID, conn)
			push.CleanupConn(conn)
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
					mu := push.GetWriteMu(conn)
					mu.Lock()
					err := ws.WriteMessage(websocket.PingMessage, nil)
					mu.Unlock()
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
```

- [ ] **Step 3: Commit**

```bash
git add ws/handler/ws.go
git commit -m "feat(ws): add WebSocket handler with auth and upgrade"
```

---

### Task 5: Create ws/main.go — service entry point

**Files:**
- Create: `ws/main.go`

- [ ] **Step 1: Write main.go**

Create `ws/main.go`:

```go
package main

import (
	"context"
	"log"
	"strings"

	"github.com/cloudwego/hertz/pkg/app/server"
	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/auth/authservice"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/pkg/telemetry"
	"eigenflux_server/ws/handler"
)

func main() {
	cfg := config.Load()
	logFlush := logger.Init("WSService", cfg.EffectiveLokiURL(), cfg.LogLevel)
	defer logFlush()

	shutdown, err := telemetry.Init("WSService", cfg.OtelExporterEndpoint, cfg.MonitorEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	// Redis (for Pub/Sub subscription).
	db.InitRedis(cfg.RedisAddr, cfg.RedisPassword)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)

	// etcd resolver for RPC clients.
	etcdEndpoints := strings.Split(cfg.EtcdAddr, ",")
	trimmed := make([]string, 0, len(etcdEndpoints))
	for _, e := range etcdEndpoints {
		if s := strings.TrimSpace(e); s != "" {
			trimmed = append(trimmed, s)
		}
	}
	if len(trimmed) == 0 {
		trimmed = []string{"localhost:2379"}
	}

	resolver, err := etcd.NewEtcdResolver(trimmed)
	if err != nil {
		log.Fatalf("failed to create etcd resolver: %v", err)
	}

	authClient, err := authservice.NewClient("AuthService", rpcx.ClientOptions(resolver)...)
	if err != nil {
		log.Fatalf("failed to create auth client: %v", err)
	}

	pmClient, err := pmservice.NewClient("PMService", rpcx.ClientOptions(resolver)...)
	if err != nil {
		log.Fatalf("failed to create pm client: %v", err)
	}

	// Hertz HTTP server with WS route.
	wsHandler := handler.New(authClient, pmClient, db.RDB)

	listenAddr := cfg.ListenAddr(cfg.WSPort)
	h := server.Default(server.WithHostPorts(listenAddr))
	h.GET("/ws/pm", wsHandler.Serve)

	logger.Default().Info("WS service started", "addr", listenAddr)
	h.Spin()
}
```

- [ ] **Step 2: Add hertz-contrib/websocket dependency**

```bash
cd /Users/phronex/git/phro-2026/agent_network/agent_network_server
go get github.com/hertz-contrib/websocket@latest
```

- [ ] **Step 3: Verify ws service compiles**

```bash
bash scripts/common/build.sh ws
```

Expected: `Compiling ws ... OK`

- [ ] **Step 4: Commit**

```bash
git add ws/main.go go.mod go.sum
git commit -m "feat(ws): add service entry point with Hertz server"
```

---

### Task 6: Add Redis PUBLISH to PM SendPM

**Files:**
- Modify: `rpc/pm/handler.go:188,279,338`

- [ ] **Step 1: Add PUBLISH in handleNewConversation**

In `rpc/pm/handler.go`, after the existing cache invalidation line 188 (`db.RDB.Del(ctx, fmt.Sprintf("pm:fetch:%d", receiverID))`), add:

```go
	db.RDB.Del(ctx, fmt.Sprintf("pm:fetch:%d", receiverID))
	db.RDB.Publish(ctx, fmt.Sprintf("pm:push:%d", receiverID), fmt.Sprintf("%d", msgID))
```

- [ ] **Step 2: Add PUBLISH in handleReply**

After line 279 (`db.RDB.Del(ctx, fmt.Sprintf("pm:fetch:%d", receiverID))`), add:

```go
	db.RDB.Del(ctx, fmt.Sprintf("pm:fetch:%d", receiverID))
	db.RDB.Publish(ctx, fmt.Sprintf("pm:push:%d", receiverID), fmt.Sprintf("%d", msgID))
```

- [ ] **Step 3: Add PUBLISH in handleFriendPM**

After line 338 (`db.RDB.Del(ctx, fmt.Sprintf("pm:fetch:%d", req.ReceiverId))`), add:

```go
	db.RDB.Del(ctx, fmt.Sprintf("pm:fetch:%d", req.ReceiverId))
	db.RDB.Publish(ctx, fmt.Sprintf("pm:push:%d", req.ReceiverId), fmt.Sprintf("%d", msgID))
```

- [ ] **Step 4: Verify PM service still compiles**

```bash
bash scripts/common/build.sh pm
```

Expected: `Compiling pm ... OK`

- [ ] **Step 5: Commit**

```bash
git add rpc/pm/handler.go
git commit -m "feat(ws): publish Redis notification on SendPM for WS push"
```

---

### Task 7: Build and manual smoke test

**Files:**
- Create: `ws/scripts/build.sh`
- Create: `ws/scripts/start.sh`

- [ ] **Step 1: Create ws/scripts directory**

```bash
mkdir -p ws/scripts
```

- [ ] **Step 2: Write build.sh**

Create `ws/scripts/build.sh`:

```bash
#!/bin/bash
set -e
PROJECT_ROOT="$(cd "$(dirname "$0")/../.."; pwd)"
BUILD_DIR="$PROJECT_ROOT/build"
mkdir -p "$BUILD_DIR"
cd "$PROJECT_ROOT"
echo "Compiling ws..."
go build -o "$BUILD_DIR/ws" ./ws/
echo "OK → build/ws"
```

- [ ] **Step 3: Write start.sh**

Create `ws/scripts/start.sh`:

```bash
#!/bin/bash
set -e
PROJECT_ROOT="$(cd "$(dirname "$0")/../.."; pwd)"
BUILD_DIR="$PROJECT_ROOT/build"

if [[ -f "$PROJECT_ROOT/.env" ]]; then
  set -a
  source "$PROJECT_ROOT/.env"
  set +a
fi

"$BUILD_DIR/ws"
```

- [ ] **Step 4: Make scripts executable**

```bash
chmod +x ws/scripts/build.sh ws/scripts/start.sh
```

- [ ] **Step 5: Full build all services**

```bash
bash scripts/common/build.sh
```

Expected: All services compile, including ws.

- [ ] **Step 6: Commit**

```bash
git add ws/scripts/
git commit -m "feat(ws): add build and start scripts"
```

---

### Task 8: Integration tests

**Files:**
- Create: `tests/ws/ws_test.go`

- [ ] **Step 1: Create test directory**

```bash
mkdir -p tests/ws
```

- [ ] **Step 2: Write ws_test.go**

Create `tests/ws/ws_test.go`:

```go
package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"eigenflux_server/pkg/config"
	"eigenflux_server/tests/testutil"
)

func TestMain(m *testing.M) {
	testutil.RunTestMain(m)
}

func wsURL() string {
	cfg := config.Load()
	return fmt.Sprintf("ws://localhost:%d", cfg.WSPort)
}

func dialWS(t *testing.T, token string, cursor int64) *websocket.Conn {
	t.Helper()
	u, _ := url.Parse(wsURL() + "/ws/pm")
	q := u.Query()
	q.Set("token", token)
	if cursor > 0 {
		q.Set("cursor", strconv.FormatInt(cursor, 10))
	}
	u.RawQuery = q.Encode()

	conn, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("ws dial failed: %v (resp=%v)", err, resp)
	}
	return conn
}

// readPush reads the next WS message and parses it into the push envelope.
func readPush(t *testing.T, conn *websocket.Conn, timeout time.Duration) map[string]interface{} {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ws read failed: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(msg, &result); err != nil {
		t.Fatalf("ws parse failed: %v, body: %s", err, string(msg))
	}
	return result
}

func waitForWS(t *testing.T) {
	t.Helper()
	u := wsURL() + "/ws/pm?token=invalid"
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, _, err := websocket.DefaultDialer.Dial(u, nil)
		if err == nil {
			conn.Close()
			return
		}
		// Connection refused means server not up yet.
		// A close/handshake error means server IS up but rejected — that's fine.
		if websocket.IsCloseError(err) || websocket.IsUnexpectedCloseError(err) {
			return
		}
		// Check if we got an HTTP response (server up, auth rejected).
		if err != nil && err.Error() != "" {
			// If we can connect at the TCP level, server is up.
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("WS service not ready after 30s")
}

func cleanPMData(t *testing.T, agentIDs ...int64) {
	t.Helper()
	ctx := context.Background()
	rdb := testutil.GetTestRedis()
	for _, id := range agentIDs {
		testutil.TestDB.Exec("DELETE FROM private_messages WHERE sender_id = $1 OR receiver_id = $1", id)
		testutil.TestDB.Exec("DELETE FROM conversations WHERE participant_a = $1 OR participant_b = $1", id)
		rdb.Del(ctx, fmt.Sprintf("pm:fetch:%d", id))
		rdb.Del(ctx, fmt.Sprintf("pm:push:%d", id))
	}
}

// --- Test Cases ---

func TestWSAuthFailure(t *testing.T) {
	waitForWS(t)

	u := wsURL() + "/ws/pm?token=invalid_token"
	_, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if err == nil {
		t.Fatal("expected dial to fail with invalid token")
	}
	if resp != nil && resp.StatusCode != 401 {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestWSInitialPush(t *testing.T) {
	testutil.WaitForAPI(t)
	waitForWS(t)

	emails := []string{"ws_sender@test.com", "ws_receiver@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	sender := testutil.RegisterAgent(t, "ws_sender@test.com", "WS Sender", "sends messages")
	receiver := testutil.RegisterAgent(t, "ws_receiver@test.com", "WS Receiver", "receives messages")

	senderID, _ := strconv.ParseInt(sender["agent_id"].(string), 10, 64)
	receiverID, _ := strconv.ParseInt(receiver["agent_id"].(string), 10, 64)
	senderToken := sender["token"].(string)
	receiverToken := receiver["token"].(string)

	cleanPMData(t, senderID, receiverID)

	// Create a mock item and send a PM before connecting WS.
	itemID := int64(990001)
	testutil.TestDB.Exec("DELETE FROM processed_items WHERE item_id = $1", itemID)
	testutil.TestDB.Exec("DELETE FROM raw_items WHERE item_id = $1", itemID)
	now := time.Now().UnixMilli()
	testutil.TestDB.Exec(
		`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES ($1, $2, $3, $4)`,
		itemID, senderID, "ws test item", now,
	)
	testutil.TestDB.Exec(
		`INSERT INTO processed_items (item_id, status, broadcast_type, updated_at) VALUES ($1, 3, 'info', $2)`,
		itemID, now,
	)

	// Send PM via HTTP.
	sendResp := testutil.DoPost(t, "/api/v1/pm/send", map[string]interface{}{
		"receiver_id": receiver["agent_id"],
		"item_id":     strconv.FormatInt(itemID, 10),
		"content":     "hello from ws test",
	}, senderToken)
	if int(sendResp["code"].(float64)) != 0 {
		t.Fatalf("send PM failed: %v", sendResp["msg"])
	}

	// Now connect WS with cursor=0, should receive the message.
	ws := dialWS(t, receiverToken, 0)
	defer ws.Close()

	msg := readPush(t, ws, 10*time.Second)
	if msg["type"] != "pm_fetch" {
		t.Fatalf("expected type pm_fetch, got %v", msg["type"])
	}
	data := msg["data"].(map[string]interface{})
	messages := data["messages"].([]interface{})
	if len(messages) == 0 {
		t.Fatal("expected at least 1 message in initial push")
	}
	first := messages[0].(map[string]interface{})
	if first["content"] != "hello from ws test" {
		t.Fatalf("expected content 'hello from ws test', got %v", first["content"])
	}

	// Cleanup.
	testutil.TestDB.Exec("DELETE FROM processed_items WHERE item_id = $1", itemID)
	testutil.TestDB.Exec("DELETE FROM raw_items WHERE item_id = $1", itemID)
	cleanPMData(t, senderID, receiverID)
	_ = receiverToken
}

func TestWSRealtimePush(t *testing.T) {
	testutil.WaitForAPI(t)
	waitForWS(t)

	emails := []string{"ws_rt_sender@test.com", "ws_rt_receiver@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	sender := testutil.RegisterAgent(t, "ws_rt_sender@test.com", "RT Sender", "sends messages")
	receiver := testutil.RegisterAgent(t, "ws_rt_receiver@test.com", "RT Receiver", "receives messages")

	senderID, _ := strconv.ParseInt(sender["agent_id"].(string), 10, 64)
	receiverID, _ := strconv.ParseInt(receiver["agent_id"].(string), 10, 64)
	senderToken := sender["token"].(string)
	receiverToken := receiver["token"].(string)

	cleanPMData(t, senderID, receiverID)

	// Connect WS first (no pending messages).
	ws := dialWS(t, receiverToken, 0)
	defer ws.Close()

	// Brief wait for subscription to settle.
	time.Sleep(500 * time.Millisecond)

	// Create a mock item for conversation.
	itemID := int64(990002)
	testutil.TestDB.Exec("DELETE FROM processed_items WHERE item_id = $1", itemID)
	testutil.TestDB.Exec("DELETE FROM raw_items WHERE item_id = $1", itemID)
	now := time.Now().UnixMilli()
	testutil.TestDB.Exec(
		`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES ($1, $2, $3, $4)`,
		itemID, senderID, "ws rt test item", now,
	)
	testutil.TestDB.Exec(
		`INSERT INTO processed_items (item_id, status, broadcast_type, updated_at) VALUES ($1, 3, 'info', $2)`,
		itemID, now,
	)

	// Send PM via HTTP — should trigger real-time push.
	sendResp := testutil.DoPost(t, "/api/v1/pm/send", map[string]interface{}{
		"receiver_id": receiver["agent_id"],
		"item_id":     strconv.FormatInt(itemID, 10),
		"content":     "realtime push test",
	}, senderToken)
	if int(sendResp["code"].(float64)) != 0 {
		t.Fatalf("send PM failed: %v", sendResp["msg"])
	}

	msg := readPush(t, ws, 10*time.Second)
	if msg["type"] != "pm_fetch" {
		t.Fatalf("expected type pm_fetch, got %v", msg["type"])
	}
	data := msg["data"].(map[string]interface{})
	messages := data["messages"].([]interface{})
	if len(messages) == 0 {
		t.Fatal("expected at least 1 message in realtime push")
	}
	first := messages[0].(map[string]interface{})
	if first["content"] != "realtime push test" {
		t.Fatalf("expected content 'realtime push test', got %v", first["content"])
	}

	// Cleanup.
	testutil.TestDB.Exec("DELETE FROM processed_items WHERE item_id = $1", itemID)
	testutil.TestDB.Exec("DELETE FROM raw_items WHERE item_id = $1", itemID)
	cleanPMData(t, senderID, receiverID)
}

func TestWSConnectionReplacement(t *testing.T) {
	testutil.WaitForAPI(t)
	waitForWS(t)

	emails := []string{"ws_replace@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agent := testutil.RegisterAgent(t, "ws_replace@test.com", "Replace Agent", "tests replacement")
	agentID, _ := strconv.ParseInt(agent["agent_id"].(string), 10, 64)
	token := agent["token"].(string)

	cleanPMData(t, agentID)

	// First connection.
	ws1 := dialWS(t, token, 0)
	defer ws1.Close()

	time.Sleep(300 * time.Millisecond)

	// Second connection — should evict ws1.
	ws2 := dialWS(t, token, 0)
	defer ws2.Close()

	// ws1 should receive a close frame with code 4002.
	ws1.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := ws1.ReadMessage()
	if err == nil {
		t.Fatal("expected ws1 to be closed after replacement")
	}
	closeErr, ok := err.(*websocket.CloseError)
	if ok && closeErr.Code != 4002 {
		t.Fatalf("expected close code 4002, got %d", closeErr.Code)
	}

	cleanPMData(t, agentID)
}
```

- [ ] **Step 3: Verify test file compiles**

```bash
go vet ./tests/ws/
```

Expected: No errors.

- [ ] **Step 4: Commit**

```bash
git add tests/ws/ws_test.go
git commit -m "test(ws): add integration tests for WebSocket PM push"
```

---

### Task 9: Run full test suite and fix issues

- [ ] **Step 1: Start all services including ws**

```bash
bash scripts/local/start_local.sh
```

- [ ] **Step 2: Run WS tests**

```bash
go test -v ./tests/ws/ -timeout 120s
```

Expected: All 4 tests pass (TestWSAuthFailure, TestWSInitialPush, TestWSRealtimePush, TestWSConnectionReplacement).

- [ ] **Step 3: Run existing PM tests to verify no regression**

```bash
go test -v ./tests/pm/ -timeout 120s
```

Expected: All existing PM tests pass.

- [ ] **Step 4: Run full test suite**

```bash
go test -v ./tests/... -timeout 300s
```

Expected: All tests pass.

- [ ] **Step 5: Commit any test fixes if needed**

```bash
git add -A
git commit -m "fix(ws): test adjustments after integration run"
```

---

### Task 10: Update documentation

**Files:**
- Modify: `docs/dev/configuration.md`
- Modify: `docs/dev/pm.md`

- [ ] **Step 1: Add WS config to configuration.md**

Add a new row to the ports table in `docs/dev/configuration.md`:

| Variable | Default | Description |
|----------|---------|-------------|
| `WS_PORT` | 8088 | WebSocket push service port |

- [ ] **Step 2: Add WebSocket section to pm.md**

Add a new section to `docs/dev/pm.md` describing the real-time push mechanism:

```markdown
## Real-Time Push (WebSocket)

The `ws/` service provides real-time PM delivery over WebSocket, deployed at `stream.eigenflux.ai` (port 8088).

**Connection:** `wss://stream.eigenflux.ai/ws/pm?token=<access_token>&cursor=<last_msg_id>`

**Flow:**
1. Client connects with auth token and optional cursor
2. Server validates token via Auth RPC, upgrades to WebSocket
3. On connect, server fetches and pushes any pending messages
4. When a new PM is sent, PM service publishes to Redis `pm:push:{receiverID}`
5. WS service receives notification, calls FetchPM, pushes to client

**Push format:**
The `data` field matches the existing `GET /api/v1/pm/fetch` response format.

**Close codes:**
- 4001: Unauthorized (invalid/expired token)
- 4002: Replaced (another connection opened for same agent)

**Heartbeat:** Server pings every 30s, expects pong within 45s.

Only one active connection per agent. New connections replace old ones.
```

- [ ] **Step 3: Update CLAUDE.md directory table**

Add `ws/` to the directory responsibilities table in CLAUDE.md:

```
| `ws/` | WebSocket push service | Hertz-based WebSocket server (port 8088). Real-time PM push via Redis Pub/Sub. Deployed at stream.eigenflux.ai |
```

- [ ] **Step 4: Commit**

```bash
git add docs/dev/configuration.md docs/dev/pm.md CLAUDE.md
git commit -m "docs: add WebSocket PM push service documentation"
```
