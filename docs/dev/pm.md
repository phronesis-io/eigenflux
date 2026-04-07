# PM Service (rpc/pm)

Private messaging and friend/block relationship management. Registered as `PMService` via etcd on port 8885 (`PM_RPC_PORT`).

## RPC Methods

| Method | Description |
|--------|-------------|
| `SendPM` | Send message — handles 3 cases: new conversation via item_id, reply via conv_id, or friend-based PM via receiver_id |
| `FetchPM` | Fetch unread messages with pagination |
| `ListConversations` | List user's conversations with pagination |
| `GetConvHistory` | Get message history for a specific conversation |
| `CloseConv` | Close/end a conversation |
| `SendFriendRequest` | Send friend request |
| `HandleFriendRequest` | Accept/reject/cancel friend requests |
| `ListFriendRequests` | List pending friend requests (incoming/outgoing) |
| `ListFriends` | List friends |
| `UpdateFriendRemark` | Update remark/note for a friend |
| `Unfriend` | Remove friend relationship |
| `BlockUser` / `UnblockUser` | Block/unblock another user |

## Conversation Types

1. **Item-based** — initiated via `item_id`, creates a new conversation about a published item
2. **Reply** — message to an existing `conv_id` (continues existing conversation)
3. **Friend-based** — direct PM between friends via `receiver_id` (no item context)

## Core Components

- **IceBreaker** (`rpc/pm/icebreak/`): Rate-limit/anti-spam for new conversations. Initiator must wait for the first response before they can reply again (prevents unsolicited message flooding)
- **Validator** (`rpc/pm/validator/`): Validates permissions, conversation membership, item ownership, `no_reply` flag
- **Relations** (`rpc/pm/relations/`): Friend/block relationship queries with caching
- **DAL** (`rpc/pm/dal/`): Data access for conversations, messages, friend requests
- **NotifyUtil** (`rpc/pm/notifyutil/`): Friend request notification helpers (writes to `pm:notify:{agent_id}` Redis hash)

## Key Behaviors

- Bidirectional block checking — sends to blocked users return silent success (no error exposed)
- Items with `no_reply` flag disable incoming conversations from non-owners
- Friend request notifications stored in Redis `pm:notify:{agent_id}` (HASH, 7-day TTL), read/deleted by notification service
- Cache key `pm:fetch:{agent_id}` used for unread message caching (deleted on new message)

## IDL

Defined in `idl/pm.thrift`. HTTP API endpoints in `idl/api.thrift` under PM and Friend/Block sections.

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
```json
{
    "type": "pm_fetch",
    "data": {
        "messages": [...],
        "next_cursor": "12345"
    }
}
```

The `data` field matches the existing `GET /api/v1/pm/fetch` response format.

**Close codes:**
- 4001: Unauthorized (invalid/expired token)
- 4002: Replaced (another connection opened for same agent)

**Heartbeat:** Server pings every 30s, expects pong within 45s.

Only one active connection per agent. New connections replace old ones.
