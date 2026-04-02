# Replay Log for Recall/Ranking Context Reconstruction

## Purpose

Add an append-only replay log that captures the full ranking context at feed serve time — user features, item features, ES scores, and positions. This data enables offline training of ranking models by reconstructing what the system "saw" when it made each ranking decision. Feedback is recorded separately and joined at export/training time.

## Data Model

Single denormalized table. Each row = one (feed request, served item) pair.

```sql
CREATE TABLE replay_logs (
    id              BIGINT PRIMARY KEY,
    request_id      BIGINT NOT NULL,
    agent_id        BIGINT NOT NULL,
    item_id         BIGINT NOT NULL,
    agent_features  JSONB NOT NULL,
    item_features   JSONB NOT NULL,
    item_score      DOUBLE PRECISION,
    position        INT NOT NULL,
    served_at       BIGINT NOT NULL,
    created_at      BIGINT NOT NULL
);

CREATE INDEX idx_replay_logs_agent_served ON replay_logs (agent_id, served_at);
CREATE INDEX idx_replay_logs_request ON replay_logs (request_id);
CREATE INDEX idx_replay_logs_item ON replay_logs (item_id, served_at);
```

- `id`: Snowflake-generated primary key.
- `request_id`: Snowflake ID grouping all items from the same feed request.
- `agent_features`: JSONB snapshot of the user's profile at serve time: `{"keywords": [...], "domains": [...], "geo": "..."}`.
- `item_features`: JSONB snapshot of the ES document fields: `{"broadcast_type", "domains", "keywords", "geo", "source_type", "quality_score", "group_id", "lang", "timeliness", "updated_at", "created_at"}`.
- `item_score`: The `_score` from ES `function_score` query.
- `position`: 0-indexed rank position in the served feed response.
- `served_at`: Unix millisecond timestamp of when the feed was served.
- `created_at`: Unix millisecond timestamp of row insertion (may lag `served_at` due to async write path).
- No foreign keys — append-only log table.

### Retention

Manual cleanup. No automatic purge. Future migration to Hive/OSS for long-term storage.

### Feedback Joining

Feedback is NOT stored in this table. Feedback arrives via `POST /api/v1/items/feedback` and is published to `stream:item:stats`. At training export time, replay logs and feedback events are joined by `(agent_id, item_id, timestamp proximity)`. This keeps the two write paths independent and allows the feedback API to evolve (new feedback types, general feedback API) without affecting the replay log schema.

## Write Path

```
FeedService (rpc/feed/handler.go)
    │  fire-and-forget goroutine (alongside existing recordImpressions)
    ▼
Redis Stream: stream:replay:log
    │  consumer group: cg:replay:log
    ▼
ReplayConsumer (pipeline/consumer/replay_consumer.go)
    │  deserialize → explode items array → batch INSERT
    ▼
PostgreSQL: replay_logs
```

### Event Message Format

One message per feed request, published to `stream:replay:log`:

```json
{
  "request_id": "1234567890",
  "agent_id": "9876543210",
  "agent_features": "{\"keywords\":[\"ai\"],\"domains\":[\"tech\"],\"geo\":\"US\"}",
  "served_at": "1743580800000",
  "items": "[{\"item_id\":\"111\",\"item_features\":\"{...}\",\"score\":12.5,\"position\":0}, ...]"
}
```

Message body is `map[string]interface{}` with string values, consistent with existing stream patterns (`stream:item:publish`, `stream:item:stats`).

### Toggle

`ENABLE_REPLAY_LOG` environment variable (default `true`). When `false`, FeedService skips publishing to the replay stream.

## SortService IDL Change

Currently `SortItemsResp` returns only `item_ids`. Extended to carry per-item scores and feature snapshots:

```thrift
struct SortedItem {
    1: required i64 item_id
    2: required double score
    3: optional string agent_features
    4: optional string item_features
}

struct SortItemsResp {
    1: required list<i64> item_ids
    2: optional string next_cursor
    3: optional list<SortedItem> sorted_items
}
```

- `sorted_items`: New field populated by SortService alongside existing `item_ids`.
- `agent_features`: JSON string of the profile snapshot. Same across all items in one request but included per-item for consumer simplicity.
- `item_features`: JSON string of ES document fields per item.
- `score`: ES `_score` from the `function_score` query, extracted from search result hits.
- FeedService reads `sorted_items` when present. `item_ids` remains for backward compatibility.

### SortService Handler Changes

In `rpc/sort/handler.go`:

1. After ES query, extract `_score` from each hit (already available in ES response, not currently captured).
2. Build `agent_features` JSON from the profile data already in scope.
3. Build `item_features` JSON from each ES hit document already in scope.
4. Populate `sorted_items` in the response.

### ES DAL Changes

In `rpc/sort/dal/es.go` / `es_query.go`:

- The `SearchItems` return type needs to include the ES `_score` per hit. Currently returns `[]Item` — add a `Score float64` field to the `Item` struct.

## Consumer Design

**File**: `pipeline/consumer/replay_consumer.go`

- Reads from `stream:replay:log` (consumer group `cg:replay:log`).
- Deserializes message, explodes items array into individual rows.
- Generates snowflake ID per row.
- Batch INSERTs into `replay_logs`.
- ACKs message on success.
- Max 3 retries on failure, log and skip on persistent failure.
- Follows same pattern as existing `item_consumer.go`.

**DAL**: `pipeline/consumer/dal/replay_dal.go` — batch insert function. Write-only path, no reads.

**Startup**: Registered in pipeline `main.go` alongside existing consumers.

## Changes by Component

| Component | Change | Files |
|-----------|--------|-------|
| IDL | Add `SortedItem` struct, `sorted_items` to `SortItemsResp` | `idl/sort.thrift` |
| Codegen | Regenerate kitex for sort | `kitex_gen/` |
| SortService | Extract `_score`, build feature snapshots, populate `sorted_items` | `rpc/sort/handler.go`, `rpc/sort/dal/es.go` |
| FeedService | Read `sorted_items`, publish replay event to stream | `rpc/feed/handler.go` |
| Stream constants | Use `stream:replay:log` / `cg:replay:log` at call sites | Inline in consumer and FeedService (matches existing pattern) |
| Migration | `CREATE TABLE replay_logs` + indexes | `migrations/` |
| Consumer | New `ReplayConsumer` + DAL | `pipeline/consumer/replay_consumer.go`, `pipeline/consumer/dal/replay_dal.go` |
| Pipeline startup | Register replay consumer | `pipeline/main.go` |
| Config | Add `ENABLE_REPLAY_LOG` (default `true`) | `pkg/config/config.go` |

**Not changed**: API gateway, ItemService, feedback path, console, notification service.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `ENABLE_REPLAY_LOG` | `true` | Toggle replay log publishing in FeedService |
