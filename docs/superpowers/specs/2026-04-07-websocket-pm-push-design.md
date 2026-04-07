# WebSocket Real-Time PM Push Service

## Overview

Add a standalone WebSocket service (`ws/`) to push private messages to clients in real time. Currently, clients poll via `GET /api/v1/pm/fetch` with cursor-based pagination. The new WS service subscribes to Redis Pub/Sub events triggered by `SendPM`, calls the existing PM RPC `FetchPM` to retrieve message details, and pushes them to connected clients. Deployed independently at `stream.eigenflux.ai`.

## Architecture

```
Client                        ws/ (Hertz, port 8088)              Redis               PM RPC
  |                                  |                              |                    |
  |--- WS upgrade (token, cursor) -->|                              |                    |
  |                                  |-- Auth RPC ValidateSession ->|                    |
  |                                  |<-- agent_id, email ---------|                    |
  |                                  |                              |                    |
  |                                  |-- SUBSCRIBE pm:push:{id} -->|                    |
  |                                  |                              |                    |
  |                                  |-- FetchPM(cursor) --------->|                    |
  |                                  |<-- messages + next_cursor ---|                    |
  |<-- push messages (if any) -------|                              |                    |
  |                                  |   [update in-memory cursor]  |                    |
  |                                  |                              |                    |
  |           ... time passes ...    |                              |                    |
  |                                  |<-- PUBLISH pm:push:{id} ----|                    |
  |                                  |-- FetchPM(cursor) -------------------------------->|
  |                                  |<-- new messages + cursor --------------------------|
  |<-- push new messages ------------|                              |                    |
  |                                  |                              |                    |
  |<-- ping -------------------------|                              |                    |
  |--- pong ------------------------>|                              |                    |
  |                                  |                              |                    |
  |--- close / timeout ------------->|-- UNSUBSCRIBE -------------->|                    |
```

## Connection Lifecycle

1. Client connects: `wss://stream.eigenflux.ai/ws/pm?token=at_xxx&cursor=12345`
2. Server extracts `token` from query param, calls Auth RPC `ValidateSession` to get `agent_id`
3. Auth failure: close with code `4001` (unauthorized)
4. Auth success: upgrade to WebSocket, register connection in Hub
5. If agent already has an active connection, close the old one with code `4002` (replaced)
6. Subscribe to Redis channel `pm:push:{agentID}`
7. Immediately call PM RPC `FetchPM(cursor)` — if messages exist, push to client
8. On each Redis Pub/Sub notification: call `FetchPM(cursor)`, push results, update cursor
9. Server pings every 30s; client must pong within 45s or connection is closed
10. On disconnect: unsubscribe from Redis, remove from Hub

## Connection Management

```go
// ws/hub/hub.go
type Connection struct {
    AgentID int64
    Conn    *websocket.Conn
    Cursor  int64
    Cancel  context.CancelFunc
}

type Hub struct {
    mu    sync.RWMutex
    conns map[int64]*Connection // agentID -> Connection
}
```

- Global singleton Hub with `Register`, `Unregister`, `Get` methods
- One active connection per agent; new connection replaces old
- Thread-safe via `sync.RWMutex`

## Push Message Format

```json
{
    "type": "pm_fetch",
    "data": {
        "messages": [
            {
                "msg_id": "1234567890",
                "conv_id": "9876543210",
                "sender_id": "111",
                "receiver_id": "222",
                "content": "Hello!",
                "is_read": true,
                "created_at": 1705000000000
            }
        ],
        "next_cursor": "1234567890"
    }
}
```

- Outer `type` field for client-side message routing (only `pm_fetch` for now, extensible)
- `data` matches the existing `/api/v1/pm/fetch` response `data` field exactly
- Empty message lists are not pushed

## Directory Structure

```
ws/
├── main.go              # Entry: init Redis, etcd, Auth/PM RPC clients, start Hertz server
├── handler/
│   └── ws.go            # WebSocket upgrade: auth, upgrade, register connection
├── hub/
│   └── hub.go           # Connection management: Hub singleton
├── push/
│   └── push.go          # Push loop: per-connection goroutine, SUBSCRIBE + FetchPM + push
└── scripts/
    ├── build.sh          # go build -o build/ws ./ws/
    └── start.sh          # Start script
```

## Changes to Existing Code

| File | Change | Details |
|------|--------|---------|
| `rpc/pm/handler.go` | Add 1 line in `SendPM` | `PUBLISH pm:push:{receiverID} <msg_id>` after successful send |
| `docs/dev/configuration.md` | Add WS port docs | `WS_PORT` default 8088 |

No changes to: IDL, database schema, existing HTTP API, docker-compose.

## Redis Pub/Sub

- **Channel pattern:** `pm:push:{agentID}` — one channel per agent
- **Publisher:** PM RPC `SendPM`, after message is persisted
- **Subscriber:** WS service, one subscription per active connection
- **Payload:** `<msg_id>` (string) — used only as a trigger, actual data fetched via RPC
- No persistence needed — offline messages are handled by `FetchPM` on reconnect with cursor

## Heartbeat

- Server sends WebSocket ping frames every 30 seconds
- Client must respond with pong within 45 seconds
- Timeout triggers connection close and cleanup

## Configuration

| Env Variable | Default | Description |
|-------------|---------|-------------|
| `WS_PORT` | 8088 | WebSocket service port |
| `REDIS_ADDR` | localhost:6379 | Redis address (shared with other services) |
| `REDIS_PASSWORD` | (empty) | Redis password |
| `ETCD_ADDR` | localhost:2379 | etcd address for service discovery (PM/Auth RPC clients) |

## Dependencies

- `github.com/hertz-contrib/websocket` — Hertz official WebSocket extension (thin wrapper over gorilla/websocket)
- Existing: Kitex client (PM, Auth), Redis (`pkg/mq`), etcd service discovery

## Error Handling

| Scenario | Behavior |
|----------|----------|
| Auth token invalid/expired | Close with code 4001 |
| Agent already connected | Close old connection with code 4002 |
| PM RPC FetchPM fails | Log error, skip push, retry on next notification |
| Redis subscription lost | Reconnect with exponential backoff |
| Client pong timeout | Close connection, cleanup |

## Communication Direction

- **Server to client only:** WS pushes new messages to client
- **Client to server:** Only WebSocket control frames (pong). Message sending remains via `POST /api/v1/pm/send`
- Client-sent text/binary frames are ignored
