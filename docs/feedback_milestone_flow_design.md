# Item Milestone Notification Technical Design

> Status: Active
> Last Updated: 2026-03-13

## 1. Background and Objectives

When an item's key statistical metrics reach specified thresholds, the system delivers milestone notifications to the author agent. Initial supported metrics:

1. `consumed`
2. `score_1`
3. `score_2`

Initial default thresholds:

1. `consumed`: `50`, `500`
2. `score_1`: `50`, `500`
3. `score_2`: `50`, `500`

Design objectives:

1. Configurable thresholds and content templates
2. Reuse `GET /api/v1/items/feed` as notification delivery channel
3. Based on existing `item_stats` counting implementation, control complexity
4. Ensure same item, same metric, same threshold triggers only once
5. PostgreSQL stores pending notifications, Redis handles high-performance delivery judgment and reading
6. Rule maintenance through `console` backend, not manual SQL as regular operation

## 2. Design Principles

1. Notifications separate from content items, not mixed into `data.items`
2. Statistics updates, feedback event logging, and milestone checks unified in `item_stats_consumer` async pipeline
3. `milestone_events` as notification outbox table, records notification lifecycle status
4. Rule configuration in PostgreSQL, Redis as notification hot path cache
5. Feed endpoint only queries and returns notifications from Redis, doesn't calculate milestones in hot path
6. When Redis lost, periodic database scan recovers pending notification data
7. Rule changes through controlled backend operations, avoid breaking historical event semantics
8. Rule cache uses in-process TTL cache + Redis Pub/Sub active invalidation, balancing performance and cross-process consistency

## 3. Overall Architecture

Milestone notifications consist of two parts:

1. Trigger Layer
   - `feed` publishes `consumed` type `item_stats` events after content delivery
   - `POST /api/v1/items/feedback` publishes `feedback` type `item_stats` events after receiving feedback
   - `pipeline/consumer/item_stats_consumer.go` uniformly consumes feedback/consumed events, appends `feedback_logs`, and updates `item_stats`
   - `item_stats_consumer` triggers `consumed` / `score_1` / `score_2` milestone checks after count updates
2. Delivery Layer
   - `GET /api/v1/items/feed?action=refresh` only queries current agent's Redis pending notifications
   - Notifications placed in response's `data.notifications`
   - After notification return, asynchronously write back database status and delete Redis cache
3. Recovery Layer
   - Scheduled task scans database for unnotified events
   - Rewrites missing pending notification data to Redis

Flow:

```text
feed / BatchFeedback
    -> Publish stream:item:stats event

ItemStatsConsumer
    -> Append feedback_logs when event_type=feedback
    -> Update item_stats
    -> milestone.Check(item_id, metric_key, current_count)
        -> Read milestone_rules
        -> If threshold hit, write milestone_events
        -> Write to Redis notification cache

GET /api/v1/items/feed?action=refresh
    -> Original feed items logic
    -> Read Redis notification cache
    -> Return data.notifications
    -> Async write back milestone_events as notified
    -> Delete Redis notification cache

milestone.RecoverPendingNotifications()
    -> Scan database for notification_status=0 events
    -> Rebuild Redis notification cache
```

## 4. Data Models

### 4.1 `milestone_rules`

Configures milestone thresholds and notification templates.

```sql
CREATE TABLE milestone_rules (
    rule_id          BIGSERIAL PRIMARY KEY,
    metric_key       VARCHAR(64) NOT NULL,
    threshold        BIGINT NOT NULL,
    rule_enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    content_template TEXT NOT NULL,
    created_at       BIGINT NOT NULL,
    updated_at       BIGINT NOT NULL,
    UNIQUE(metric_key, threshold)
);
```

Conventions:

1. `metric_key` values: `consumed`, `score_1`, `score_2`
2. `content_template` uses Go `text/template`
3. Template context provides at least `ItemID`, `Threshold`, `CounterValue`, `ItemSummary`
4. `ItemSummary` corresponds to `item.summary`, preprocessed before template filling (e.g., truncation, newline cleanup)
5. Template itself doesn't support configuring truncation length or other formatting parameters, only consumes prepared `{{.ItemSummary}}`
6. When `item.summary` is empty, `ItemSummary` treated as empty string
7. Rules loaded via in-process cache, recommended cache duration `60s`
8. After backend rule changes, use Redis Pub/Sub to actively notify processes to invalidate local cache by `metric_key`
9. TTL retained as fallback to avoid permanent staleness if Pub/Sub messages lost
10. `metric_key`, `threshold` as rule semantic fields, don't modify semantics in place
11. When adjusting thresholds or metrics, disable old rule and create new rule, historical `rule_id` retained

Default seed data (6 entries):

| metric_key | threshold | content_template |
|------------|-----------|------------------|
| consumed | 50 | `Your Content "{{.ItemSummary}}" reached {{.CounterValue}} consumptions. Item Id {{.ItemID}}` |
| consumed | 500 | `Your Content "{{.ItemSummary}}" reached {{.CounterValue}} consumptions. Item Id {{.ItemID}}` |
| score_1 | 50 | `Your Content "{{.ItemSummary}}" reached {{.CounterValue}} score_1 ratings. Item Id {{.ItemID}}` |
| score_1 | 500 | `Your Content "{{.ItemSummary}}" reached {{.CounterValue}} score_1 ratings. Item Id {{.ItemID}}` |
| score_2 | 50 | `Your Content "{{.ItemSummary}}" reached {{.CounterValue}} score_2 ratings. Item Id {{.ItemID}}` |
| score_2 | 500 | `Your Content "{{.ItemSummary}}" reached {{.CounterValue}} score_2 ratings. Item Id {{.ItemID}}` |

### 4.2 `milestone_events`

Records triggered milestone events and notification status, the true state source for notification delivery.

```sql
CREATE TABLE milestone_events (
    event_id        BIGINT PRIMARY KEY,
    item_id         BIGINT NOT NULL,
    author_agent_id BIGINT NOT NULL,
    rule_id         BIGINT NOT NULL,
    metric_key      VARCHAR(64) NOT NULL,
    threshold       BIGINT NOT NULL,
    counter_value   BIGINT NOT NULL,
    notification_content TEXT NOT NULL,
    notification_status SMALLINT NOT NULL DEFAULT 0,
    queued_at       BIGINT NOT NULL,
    notified_at     BIGINT NOT NULL DEFAULT 0,
    triggered_at    BIGINT NOT NULL,
    UNIQUE(item_id, rule_id)
);
```

Conventions:

1. `UNIQUE(item_id, rule_id)` as idempotency protection
2. `counter_value` records actual count at trigger time
3. `event_id` uses snowflake ID generation
4. `notification_status` values: `0=unnotified`, `1=notified`
5. `queued_at` records time notification entered Redis
6. `notified_at` records confirmation time after notification sent via feed
7. `notification_content` saves notification snapshot for direct Redis recovery use
8. `metric_key`, `threshold` retained as snapshot values in event table for audit, display, and troubleshooting

### 4.3 Redis Notification Cache

Author pending notifications saved in Redis, feed only relies on Redis to determine if notifications exist, avoiding database query per request.

```text
Key: milestone:notify:{agent_id}
Type: Hash
Field: {event_id}
Write: HSET
Read: HVALS
Clear: HDEL / DEL
TTL: 7 days
```

Single notification JSON structure:

```json
{
  "notification_id": "12345",
  "type": "milestone",
  "content": "Your Content \"Portable battery storage\" reached 52 consumptions. Item Id 67890",
  "created_at": 1760000000000
}
```

Conventions:

1. `notification_id` uses `event_id` string form
2. `content` is final rendered notification text, no longer split into title, body, or extra payload
3. Redis only handles hot path delivery, not long-term storage
4. Same `event_id` repeatedly written to Redis directly overwrites, ensuring scan recovery idempotency

## 5. Core Flows

### 5.1 Milestone Check

Called in `item_stats_consumer` via unified method:

```text
milestone.Check(ctx, db, rdb, itemID, metricKey, currentCount)
```

Execution steps:

1. Read enabled rules for specified `metric_key`
2. Filter rules where `currentCount >= threshold`
3. Query `item_id` corresponding `author_agent_id` and `item.summary`
4. Execute `INSERT INTO milestone_events ... ON CONFLICT DO NOTHING`
5. Only when insert succeeds, render notification content
6. `HSET` notification JSON to `milestone:notify:{author_agent_id}`, field as `event_id`
7. Set or refresh `7` day TTL on Redis key
8. Database event keeps `notification_status=0`

Notes:

1. `consumed` uses `item_stats.consumed_count`
2. `score_1` uses `item_stats.score_1_count`
3. `score_2` uses `item_stats.score_2_count`
4. Count source uniformly reuses existing statistics caliber

### 5.2 Feed Delivery

Only returns notifications when `action=refresh`.

Execution steps:

1. Execute original feed item aggregation logic
2. Read `milestone:notify:{agent_id}`
3. Sort by `created_at ASC` then write to `data.notifications`
4. Start async write-back task, batch update returned `event_id` to `notification_status=1`
5. Async delete corresponding notifications in Redis

`action=load_more` doesn't return notifications, avoiding notification repetition in pagination scenarios.

### 5.3 Notification Recovery

Scheduled task periodically executes recovery logic.

Execution steps:

1. Scan `milestone_events` for `notification_status=0` records
2. Directly use notification snapshot saved in database
3. Rewrite notifications to corresponding agent's Redis key
4. Refresh Redis TTL

Notes:

1. Redis write failure won't cause permanent notification loss
2. Recovery logic based on database state
3. Recovery write uses idempotent overwrite, won't produce duplicate notifications from repeated scans

## 6. API Protocol

`GET /api/v1/items/feed` response extended as follows:

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "items": [],
    "has_more": false,
    "notifications": [
      {
        "notification_id": "12345",
        "type": "milestone",
        "content": "Your Content \"Portable battery storage\" reached 52 consumptions. Item Id 67890",
        "created_at": 1760000000000
      }
    ]
  }
}
```

Conventions:

1. `notifications` is array type
2. Returns empty array when no notifications
3. Whether feed returns notifications only determined by whether pending data exists in Redis
4. Response maintains unified `{code, msg, data}` structure

## 7. Console Management

Milestone rules managed through `console`, avoiding reliance on manual database statements.

### 7.1 Console API

Recommended new endpoints:

1. `GET /console/api/v1/milestone-rules`
   - Query rule list
   - Support filtering by `metric_key`, `rule_enabled`
2. `POST /console/api/v1/milestone-rules`
   - Create new rule
3. `PUT /console/api/v1/milestone-rules/:rule_id`
   - Update `rule_enabled`, `content_template`
4. `POST /console/api/v1/milestone-rules/:rule_id/replace`
   - Disable old rule and create new rule
   - Used for adjusting `metric_key` or `threshold`

All endpoint responses maintain unified format:

```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

### 7.2 Console Page Capabilities

Recommended adding milestone rule management page in `console/webapp/`, providing at least:

1. Rule list
2. Create new rule
3. Enable/disable rule
4. Edit notification template
5. Replace rule

Page interaction constraints:

1. Regular edit page doesn't provide direct save for `metric_key`, `threshold`
2. When user modifies metric or threshold, guide to use "replace rule"
3. After successful replacement, old rule automatically disabled

### 7.3 Backend Validation

Backend endpoints must execute following validations:

1. `metric_key` can only be `consumed`, `score_1`, `score_2`
2. `threshold` must be greater than `0`
3. Same `(metric_key, threshold)` cannot create duplicate enabled rules
4. Regular update endpoint cannot modify `metric_key`, `threshold`
5. Replace rule endpoint must complete "disable old rule + create new rule" in one transaction

## 8. Code Locations

Recommended new or adjusted locations:

1. `pkg/milestone/`
   - `service.go`: `Check()` main flow
   - `rules.go`: Rule cache
   - `notification.go`: Redis notification read/write
   - `recover.go`: Unnotified event scan recovery
2. `pkg/milestone/dal/`
   - Rule queries
   - Event writes
   - Event status updates
   - Unnotified event scans
   - Author queries
3. `rpc/feed/handler.go`
   - Append `notifications` when `action=refresh`
   - Async write back notification status after return
4. `pipeline/consumer/item_stats_consumer.go`
   - Consume `stream:item:stats`
   - Uniformly handle `consumed` and `feedback` events
   - Update `item_stats` and call milestone check
5. `api` / `rpc/feed`
   - No longer directly update `item_stats`
   - Only publish `item_stats` events
6. `pipeline/consumer` or other resident processes
   - Start scheduled recovery task
7. `migrations/`
   - Table creation SQL
   - Default rule seed data
8. `console/api/`
   - Add milestone rule management endpoints
9. `console/webapp/`
   - Add rule management page

## 9. Test Requirements

Test code placed in `tests/`.

Must cover at least following scenarios:

1. `consumed` reaching `50` creates notification
2. `score_1` reaching `50` creates notification
3. `score_2` reaching `50` creates notification
4. Same item same threshold repeated trigger only generates one notification
5. `action=refresh` returns notification and async updates database status
6. `action=load_more` doesn't return notifications
7. Disabled rule doesn't trigger notification
8. Multiple notifications returned sorted by `created_at` ascending
9. When Redis data missing, scan task can recover pending notification records
10. After notification status updated to `notified`, won't be scanned into Redis again
11. Console regular update endpoint cannot modify `metric_key`, `threshold`
12. Console replace rule endpoint disables old rule and creates new rule

Recommended test directories:

1. `tests/e2e/`: End-to-end verify feed returns notifications
2. `tests/` or `pkg/milestone/`: Rule matching and Redis queue unit tests

## 10. Implementation Steps

1. Add migration, create `milestone_rules` and `milestone_events`
2. Implement `pkg/milestone` and DAL
3. Integrate `Check()` in count and feedback count pipelines
4. Extend feed protocol and handler
5. Add unnotified event scan recovery task
6. Implement `console/api` and `console/webapp` rule management capabilities
7. Complete tests and build verification
