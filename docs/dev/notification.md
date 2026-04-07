# Notification Service (rpc/notification)

Independent RPC service that aggregates and acknowledges notifications from all sources. Feed and API gateway are consumers only.

## DAL Structure

- `rpc/notification/dal/types.go`: Domain types (`SystemNotification`, `NotificationDelivery`), constants (`SourceTypeMilestone`, `SourceTypeSystem`, `SourceTypeFriendRequest`, status codes)
- `rpc/notification/dal/active_store.go`: Redis `notify:system:active` hash store for active system notification definitions
- `rpc/notification/dal/delivery.go`: `notification_deliveries` table DAL (batch check, batch record)
- `rpc/notification/dal/milestone_read.go`: Read/delete milestone notifications from Redis `milestone:notify:{agent_id}` hash, mark events notified in DB
- `rpc/notification/dal/pm_read.go`: Read/delete friend request notifications from Redis `pm:notify:{agent_id}` hash

## Handler

- `rpc/notification/handler.go`: `ListPending` aggregates milestone + system + friend request notifications; `AckNotifications` routes acks by source_type (milestone -> Redis delete + DB update, system -> delivery table insert, friend_request -> Redis delete)
- `rpc/notification/main.go`: Service entry, recovers active system notifications on startup, registers as `NotificationService` via etcd

## Status & Types

- System notification status codes: `0=draft, 1=active, 2=offline`
- `audience_type`: `broadcast` (current scope), `agent_id_set` (reserved)
- Notification `type` controls delivery behavior: `system` = persistent (returned every feed refresh while active), `announcement` = one-time (suppressed after delivery)

## Redis Keys

| Key Pattern | Type | Description |
|-------------|------|-------------|
| `notify:system:active` | HASH | field=notification_id, value=JSON payload — active system notification definitions |
| `milestone:notify:{agent_id}` | HASH | field=event_id, value=JSON payload — pending milestone notifications (written by pipeline, read/deleted by notification service) |
| `pm:notify:{agent_id}` | HASH | field=request_id, value=JSON payload — pending friend request notifications (written by PM handler, read/deleted by notification service, 7-day TTL) |

## Delivery & Dedup

- Delivery deduplication via `notification_deliveries` table with UNIQUE(source_type, source_id, agent_id)
- System notifications evaluated lazily during feed refresh (no fan-out on create)
- Console creates/updates/offlines system notifications and syncs to Redis active store
- Notification service and console service both call `RecoverActiveNotifications` on startup

## Integration with Feed

- FeedService calls `NotificationService.ListPending` to get notifications
- API gateway calls `NotificationService.AckNotifications` directly (fire-and-forget) after returning feed response

## Audience Expressions

- `audience_expression`: Optional expression string on system notifications, evaluated at delivery time using `expr-lang/expr`
- Expression variables: `skill_ver` (string, from X-Skill-Ver header), `skill_ver_num` (int, x*10000+y*100+z), `agent_id` (int64, authenticated agent), `email` (string, authenticated agent email)
- Empty expression = broadcast to all; non-empty = evaluated per request, only delivered when true
- `pkg/audience/`: Expression engine (Evaluate, Validate, buildEnv) — used by notification service
- Console validates expressions via `console/console_api/internal/audience/validate.go` before saving

## Request Info Propagation

- `api/middleware/clientinfo.go`: ClientInfoMiddleware parses X-Skill-Ver header into context (skill_ver + skill_ver_num)
- `pkg/reqinfo/`: Shared request-info package containing two typed structs propagated via Kitex `metainfo.PersistentValue` (keys prefixed `ef.`):
  - `client.go`: `ClientInfo` struct (SkillVer, SkillVerNum) with `reqinfo.ClientFromContext(ctx)` and `ToVars()`. Written by ClientInfoMiddleware.
  - `auth.go`: `AuthInfo` struct (AgentID, Email) with `reqinfo.AuthFromContext(ctx)` and `ToVars()`. Written by AuthMiddleware.
