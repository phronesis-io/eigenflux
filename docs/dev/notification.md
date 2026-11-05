# Notification Service (rpc/notification)

Independent RPC service that aggregates and acknowledges notifications from all sources. Feed and API gateway are consumers only.

## Commission Order Inbox

Commission Order lifecycle notifications use PostgreSQL `notification_inbox` as the authoritative pending and acknowledgement store. Commission publishes a schema-v1 immutable fact to `stream:commission:notification`; Pipeline validates the envelope, inserts by recipient-scoped dedupe key, and only then publishes a best-effort `notification:push:{agent_id}` wake-up. Redis Pub/Sub never owns notification state.

`ListPending` returns every unacknowledged Order version with `source_type=commission_order` and `payload_json`, ordered with the other sources by creation time and notification ID. Clients explicitly acknowledge after successful processing. Commission acknowledgements are Agent-scoped, transactional, idempotent, and return a non-zero response on persistence or ownership failure. Legacy Feed auto-ack excludes this source.

Pending rows expire after 90 days. Acknowledged rows remain for 180 days to preserve deduplication and audit evidence.

Workflow result notices reuse schema v1 and `source_type=commission_order`.
Refund, settlement and validation processing errors preserve the current order
state/version. Settlement completion confirms seller wallet credit; refund
completion covers cancelled orders paid late. Ordinary refunded transitions
already provide their success notice. Dedupe keys identify order, workflow,
event kind and recipient, so different results at one order version coexist.
CLI display must prefer the event kind over state; these notices never trigger
Agent execution. Deploy updated test clients before enabling new producer events.

## Commission listing moderation inbox

`commission.moderation.changed.v1` facts share `COMMISSION_INDEX_STREAM` with
catalogue events. Under `ENABLE_COMMISSION_ORDER_NOTIFICATIONS=true`, Pipeline
runs an independent `${COMMISSION_INDEX_CONSUMER_GROUP}:moderation-notification`
group and `${COMMISSION_INDEX_DLQ_STREAM}:moderation-notification` dead-letter
stream. Moderation consumers ignore catalogue events; the index consumer ignores
moderation events. Apply migration `000131` before enabling the new consumer.

The schema-v1 envelope identifies aggregate type `commission_moderation`, the
moderation ID, and its monotonic notification version. Payload moderation,
Commission, and recipient Agent IDs are decimal strings; draft version,
notification version and occurrence time are integers. Payload `notification_version`
must equal the envelope aggregate version. Pending, review, rejection, failure, stale,
publication, manual approval and explicit seller resubmission with retained approval notices persist as `source_type=commission_moderation`.
Processing and intermediate automatic approval do not produce notices.

The durable inbox stores these facts with zero order fields and recipient-scoped
moderation/version deduplication. It commits before best-effort Pub/Sub wake-up;
missed wake-ups are recovered by pending polling. WebSocket `notification_push`
includes validated moderation payloads on connection and on inbox wake-up,
without acknowledging them. List responses preserve payload
IDs and include status plus seller guidance in `content`. Explicit ACK verifies
both source and recipient transactionally; legacy Feed auto-ACK excludes this
source. Moderation notices are informational and never trigger Order execution.
These are immutable historical transitions; same-millisecond display order is not
guaranteed across producer workers. Read the owned listing for its current state.
Unknown moderation states, malformed payloads and recipient mismatches are
excluded from pending delivery with the `notification.moderation_skipped` warning
at RPC, API and WebSocket boundaries. Warnings contain notification and Agent IDs,
never payload bodies. `moderation_notification_skipped_total{boundary}` counts
skipped delivery attempts at the fixed `rpc`, `api`, `legacy_api`, and `ws`
boundaries. Repeated reads increment again; this is not a count of unique corrupt
rows. IDs and payload content are not metric labels. RPC filters before pagination, so a page of invalid history
cannot hide valid Order or moderation notices. Skipping neither deletes nor
acknowledges the stored fact; operators can investigate and repair it, after which
it becomes deliverable. The current listing remains the source of current state.

The existing 90-day pending and 180-day acknowledged retention applies.
Migration `000131` Down intentionally refuses while any moderation history exists,
including acknowledged rows. It preserves the rows and original constraints
transactionally. Roll back application binaries while retaining the expanded
schema; do not delete notification history to force a database downgrade.

## DAL Structure

- `rpc/notification/dal/types.go`: Domain types (`SystemNotification`, `NotificationDelivery`), source constants, and status codes
- `rpc/notification/dal/inbox.go`: Durable Commission Order and moderation insert, pending list, delivery-error, and transactional ACK operations
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
- Expression variables: `skill_ver` (string, from X-Skill-Ver header), `skill_ver_num` (int, x*10000+y*100+z), `cli_ver` (string, from X-CLI-Ver header), `cli_ver_num` (int, x*10000+y*100+z), `agent_id` (int64, authenticated agent), `email` (string, authenticated agent email)
- Empty expression = broadcast to all; non-empty = evaluated per request, only delivered when true
- `pkg/audience/`: Expression engine (Evaluate, Validate, buildEnv) — used by notification service
- Console validates expressions via `console/console_api/internal/audience/validate.go` before saving

## Request Info Propagation

- `api/middleware/clientinfo.go`: ClientInfoMiddleware parses X-Skill-Ver and X-CLI-Ver headers into context
- `pkg/reqinfo/`: Shared request-info package containing two typed structs propagated via Kitex `metainfo.PersistentValue` (keys prefixed `ef.`):
  - `client.go`: `ClientInfo` struct (SkillVer, SkillVerNum, CLIVer, CLIVerNum) with `reqinfo.ClientFromContext(ctx)` and `ToVars()`. Written by ClientInfoMiddleware.
  - `auth.go`: `AuthInfo` struct (AgentID, Email) with `reqinfo.AuthFromContext(ctx)` and `ToVars()`. Written by AuthMiddleware.

Pending-notification pagination errors from the Notification RPC return HTTP 400 (`INVALID_NOTIFICATION_PAGE`). Dependency failures remain HTTP 503 and emit `notification_pending_failed` with a bounded reason and upstream code; cursor and notification payloads are not logged.

### Pending endpoint incident verification

Before attributing HTTP 503 to RPC timeouts, compare status-specific request latency and the authentication rejection logs. During the 2026-11-02 investigation, the preceding 24-hour 503 histogram placed all approximately 156 failures below 5 ms; the RPC timeout is 10 seconds. A read-only live control returned 200 without a cursor and 503 with a malformed cursor on the old gateway. This proves the invalid-pagination mapping defect, but aggregate latency alone does not identify every historical request. Keep genuine dependency errors visible and use the classified gateway logs after deployment to separate them.

A bounded access-log sample of 1,000 recent pending requests contained 978 HTTP 200 requests without cursors and 22 HTTP 503 requests with invalid cursors; no other 503 class appeared in that sample. Raw cursors and credentials are not retained in incident evidence.
