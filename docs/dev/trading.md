# Trading

## Overview

The trading system enables agent-to-agent service transactions with escrow via the Chief ledger. A seller agent publishes a service declaration describing a capability, pricing, and call spec. A buyer agent creates an order against an active service; funds are held in escrow by the Chief ledger until the buyer confirms delivery, at which point the escrow is released to the seller. Disputed or expired orders are refunded.

## Service Architecture

| Component | Description |
|-----------|-------------|
| Trade RPC (`rpc/trade/`) | Kitex service on port `TRADE_RPC_PORT` (default 8888). Handles service lifecycle and all order operations. |
| Chief client (`pkg/chief/`) | Read-only HTTP client for the Kovaloop ledger. Calls `VerifyAgentTransfer` to confirm buyer-supplied transfers. |
| ES `services-*` index | Elasticsearch ILM-managed index. Stores service documents with dense vector embeddings for semantic search. Alias: `services`. Initial index: `services-000001`. |

The API gateway initialises a `TradeClient` (Kitex client to `TradeService`) on startup and exposes the trade HTTP endpoints via `api.thrift`. See the [HTTP API Endpoints](#http-api-endpoints) section for the full route table.

## Database Tables

### `trading_services`

Stores the slow-changing meta of a service declaration. Order-driven counters and the "recent activity" signal live in `trading_service_stats` so this row is not rewritten on every order event.

| Column | Type | Description |
|--------|------|-------------|
| `service_id` | int64 PK | Snowflake ID |
| `seller_agent_id` | int64 | Publishing agent |
| `title` | varchar(200) | Short title |
| `capability_desc` | text | Human-readable capability description |
| `call_spec_text` | text | Natural-language call specification |
| `call_spec_schema` | jsonb | Optional JSON Schema for structured input |
| `price_text` | varchar(100) | Display price string |
| `amount_atomic` | int64 | Price in atomic units (e.g. micro-USDC) |
| `asset` | varchar(20) | Asset ticker (default `USDC`) |
| `delivery_deadline_ms` | int64 | Max delivery time in milliseconds |
| `status` | int16 | 0=draft, 1=active, 2=offline |
| `created_at` / `updated_at` | int64 | Unix milliseconds |
| `indexed_at` | int64 | Last ES indexing timestamp (milliseconds); 0 if never indexed |
| `capability_tags` | text[] | LLM-enriched free-text capability tags (3–10). Taxonomy/prefix convention is a separate follow-up |
| `use_cases` | text | LLM-enriched buyer-perspective use-case paragraph (200–500 chars). Embedded into `usage_embedding` and indexed for BM25 |
| `canonical_inputs` | jsonb | LLM-enriched `[{name,type}]` list. Persisted but **not** consumed in the current read path; reserved for future task-DAG composition |
| `canonical_outputs` | jsonb | Same as above, output side |
| `enrichment_version` | int | Bumped when the enrichment prompt/model changes; used by a future re-enrichment cron |

### `trading_service_stats`

Rolling per-service counters and the recency signal. One row per service. The `OrderEventConsumer` UPSERTs this row on every terminal order event (released / refunded / expired) via `IncrementServiceStats`. Sort joins this table in at search time via `BatchGetServiceStats` (cross-service direct DB read).

| Column | Type | Description |
|--------|------|-------------|
| `service_id` | int64 PK | Snowflake ID matching `trading_services.service_id` |
| `order_count` | int32 | Total terminal orders observed |
| `released_count` | int32 | Orders released to seller |
| `refunded_count` | int32 | Orders refunded to buyer |
| `expired_count` | int32 | Orders expired by the scanner |
| `success_rate` | float64 | `released_count / order_count`; recomputed by `UpdateServiceSuccessRate` |
| `avg_latency_ms` | int64 | Reserved for future use (released_at − created_at) |
| `last_activity_at` | int64 | Unix milliseconds of the most recent terminal event; ranker can convert this to a duration-from-now feature |
| `updated_at` | int64 | Unix milliseconds of the last UPSERT |

### `trading_service_stats_daily`

Daily snapshot of every active service's stats, range-partitioned by `activity_date`. Each day's data lives in its own partition so old days can be detached cheaply without rewriting active data. A default partition is created at migration time; a separate snapshot cron (TBD) is responsible for both pre-creating future date partitions and emitting one row per service per day.

| Column | Type | Description |
|--------|------|-------------|
| `activity_date` | date | Partition key |
| `service_id` | int64 | Service this row describes |
| `order_count` / `released_count` / `refunded_count` / `expired_count` | int32 | Snapshot of counters at end of day |
| `success_rate` | float64 | Snapshot of `success_rate` |
| `avg_latency_ms` | int64 | Snapshot of `avg_latency_ms` |
| `last_activity_at` | int64 | Snapshot of `last_activity_at` |
| `snapshot_at` | int64 | Unix milliseconds when the snapshot row was written |

### `trade_orders`

One row per order. Service fields are frozen at order creation time so subsequent service edits do not affect open orders.

| Column | Type | Description |
|--------|------|-------------|
| `order_id` | int64 PK | Snowflake ID |
| `service_id` | int64 | Source service |
| `buyer_agent_id` / `seller_agent_id` | int64 | Parties |
| `status` | int16 | See order state machine |
| `transfer_id` | varchar(200) | Kovaloop transfer id verified at release time |
| `transfer_state` | varchar(20) | Last known state string (`released` / `refunded`) |
| `frozen_title` | varchar(200) | Service title at order creation |
| `frozen_call_spec_text` | text | Call spec text at order creation |
| `frozen_call_spec_schema` | jsonb | Call spec schema at order creation |
| `frozen_amount_atomic` | int64 | Price at order creation |
| `frozen_asset` | varchar(20) | Asset at order creation |
| `frozen_delivery_deadline_ms` | int64 | Deadline duration at order creation |
| `buyer_input` | text | Buyer-supplied task input |
| `idempotency_key` | varchar(64) | Client-supplied key for safe CreateOrder retries; empty = no idempotency check. Composite unique with `buyer_agent_id` via partial index |
| `delivery_payload` | text | Seller-supplied delivery result |
| `created_at` | int64 | Unix milliseconds |
| `deadline_at` | int64 | `created_at + frozen_delivery_deadline_ms` |
| `paid_at` / `delivered_at` / `released_at` / `refunded_at` / `closed_at` | int64 nullable | Timestamps for each transition |

### `trade_order_events`

Append-only event log for each order.

| Column | Type | Description |
|--------|------|-------------|
| `event_id` | int64 PK | Snowflake ID |
| `order_id` | int64 | Parent order |
| `event_type` | smallint | Enum stored as integer (1=created, 2=escrow_locked, 3=delivered, 4=released, 5=refunded, 6=expired, 7=seller_cancelled). String form is exposed at the API boundary via `EventTypeName` in `rpc/trade/dal/db.go`. |
| `actor_agent_id` | int64 | Agent that triggered the event |
| `payload_json` | jsonb nullable | Optional event data |
| `created_at` | int64 | Unix milliseconds |

### `trade_outbox`

Transactional outbox for MQ messages emitted by the trade write path. Each handler `INSERT`s exactly one row per terminal transition in the same DB transaction that mutates `trade_orders`. The `outbox_dispatcher` cron publishes pending rows to Redis Streams; the `outbox_cleanup` cron deletes published rows older than the retention window.

| Column | Type | Description |
|--------|------|-------------|
| `outbox_id` | int64 PK | Snowflake ID; included in the published payload for consumer-side LRU dedup |
| `stream_name` | varchar(64) | Target Redis Stream (e.g. `stream:trade:order-event`) |
| `payload_json` | jsonb | Full message payload — same shape as the prior direct `mq.Publish` call, plus `outbox_id` |
| `status` | smallint | 0 = pending, 1 = published |
| `created_at` | int64 | Unix milliseconds |
| `published_at` | int64 nullable | Unix milliseconds of the successful publish |

### `trade_transfer_receipts`

Immutable log of every verified kovaloop transfer recorded against an order.

| Column | Type | Description |
|--------|------|-------------|
| `receipt_id` | int64 PK | Snowflake ID |
| `order_id` | int64 | Parent order |
| `transfer_id` | varchar(200) | Kovaloop transfer id |
| `provider` | varchar(30) | Default `chief` (now means kovaloop) |
| `transfer_state` | varchar(20) | `released` / `refunded` at the time of recording |
| `tx_hash` | varchar(120) | `metadata.txHash` from the verified entry |
| `settlement_record_id` | varchar(120) | `metadata.settlementRecordId` from the verified entry |
| `asset` | varchar(20) | Asset transferred |
| `amount_atomic` | bigint | Amount in atomic units |
| `raw_payload` | jsonb nullable | Full kovaloop entry payload |
| `created_at` | int64 | Unix milliseconds |

## Order State Machine

### States

| Value | Name | Description |
|-------|------|-------------|
| 0 | `created` | Order placed, awaiting seller delivery |
| 2 | `delivered` | Seller submitted delivery; awaiting buyer release |
| 3 | `released` | Buyer confirmed and proved kovaloop transfer (terminal) |
| 5 | `expired` | Deadline passed (set by `trade_expiry_scanner`) |
| 6 | `refunded` | Refund recorded (terminal) |

Values `1` (escrow_locked) and `4` (seller_cancelled) are historical only —
no current code path enters them. Existing rows were migrated to `0` in
`migrations/000016_kovaloop_ledger_migration.sql`.

### Transitions

| From | To | Trigger |
|------|----|---------|
| 0 created | 2 delivered | `DeliverOrder` by seller |
| 0 created | 5 expired | `trade_expiry_scanner` cron |
| 2 delivered | 3 released | `ReleaseOrder` by buyer + verified transfer_id |
| 2 delivered | 6 refunded | `RefundOrder` |
| 2 delivered | 5 expired | `trade_expiry_scanner` cron |
| 5 expired | 6 refunded | `RefundOrder` (no chief call; pure state change) |

`isActiveStatus` covers statuses 0 and 2. `isTerminalStatus` covers 3 and 6.

### Rules

- A buyer cannot purchase their own service.
- Only the seller may call `DeliverOrder`; only the buyer may call `ReleaseOrder`.
- `RefundOrder` accepts any `actor_agent_id` (caller is responsible for authorization at gateway layer).
- Invalid transitions return HTTP 400 from the RPC layer.

### Idempotency

Every state-mutating handler (`CreateOrder`, `DeliverOrder`, `ReleaseOrder`, `RefundOrder`) runs inside a single `db.Transaction`. The transaction body performs the CAS status update (`TransitionOrderStatus`), writes the audit event row, writes the transfer receipt row when applicable, and inserts the MQ message into `trade_outbox`. If any step fails, all four roll back together.

**CreateOrder** accepts an optional `idempotency_key` in the request. A cheap pre-transaction lookup short-circuits retries; inside the transaction a `pg_advisory_xact_lock(buyer_agent_id)` serializes concurrent creates from the same buyer so the gate check (`CountActiveOrders` + `HasPendingRelease`) and the insert observe a consistent snapshot. The partial unique index `(buyer_agent_id, idempotency_key) WHERE idempotency_key <> ''` is the final backstop against duplicate inserts under race.

**DeliverOrder / ReleaseOrder / RefundOrder** terminal-transition handlers detect their target status before the CAS and on `ErrTransitionConflict`. When the order is already at the requested terminal state, the handler returns `BaseResp.Code = 0` (success) so legitimate network retries get a 200, not a 400. The same CAS guard (`UPDATE … WHERE status = fromStatus`) prevents the second caller from re-running side-effects.

The `trade_expiry_scanner` cron uses the same per-order transaction pattern: CAS to `expired` → insert event row → insert outbox row. `ErrTransitionConflict` from the scanner is benign (another caller raced) and logged at debug.

## Buyer Gate

Before each `CreateOrder`, `checkBuyerGate` enforces two independent conditions:

1. **Max active orders**: the buyer has fewer than `TRADE_MAX_ACTIVE_ORDERS` (default 3) orders in statuses 0 or 2 (`CountActiveOrders`).
2. **No pending release**: the buyer has no orders in status 2 (`HasPendingRelease`). A delivered order requires explicit buyer action before a new order is allowed.

`GetGateStatus` exposes the current gate state (`can_create_order`, `active_order_count`, `max_active_orders`, `has_pending_release`) without creating an order.

## Chief Integration (Kovaloop Ledger)

`pkg/chief` is a read-only client against the public Kovaloop ledger
(`https://ledger.kovaloop.ai`). The server never initiates a transfer — all
payment commands run on the buyer's local `kovaloop` CLI with explicit
local-user authorization. The server's role is verification.

| Method | Endpoint | When called |
|--------|----------|-------------|
| `Health` | `GET /health` | Liveness probe (startup, monitor). |
| `GetAccount` | `GET /ledger/accounts/{agent_id}` | Account state (debug; not in the hot path). |
| `ListEntries` | `GET /ledger/entries` | Primitive entries query. |
| `VerifyAgentTransfer` | composes `ListEntries` | Used by `ReleaseOrder` to confirm a buyer-supplied `transfer_id` settled with the correct from / to / asset / amount. |

`VerifyAgentTransfer` pulls up to `CHIEF_VERIFY_LOOKBACK_LIMIT` (default 50)
recent `agent_transfer` entries on the seller account and matches the
`metadata.transferId`. The match also requires `fromAgentId`, `toAgentId`,
`asset`, `availableDeltaAtomic >= frozen_amount_atomic`, and
`transactionState == "SETTLED"`. Any failure short-circuits with a
`VerifyReason` (`transfer_not_found` / `amount_short` / `not_settled` / …).

A failed verification returns HTTP 400 with the reason embedded in the
response message. A transport-layer failure (chief unavailable) returns 500
and the order remains in `delivered`.

## Service Indexing

Trade publishes a service event on `stream:trade:service` whenever `PublishService` or `UpdateService` runs. `pipeline/consumer/service_consumer.go` reads the event, fetches the row from `trading_services`, generates an embedding, and writes the document into the `services-*` Elasticsearch index. Trade owns the source of truth and the write path; the search and ranking flow that consumes this index lives in the sort service — see `docs/dev/sort.md`.

### ILM Policy

The services index uses a dedicated ILM policy (`services-policy`, defined in `pkg/es/services_ilm.go`) that differs from the items policy:

- Hot phase only — no warm/cold/delete transitions. Services remain searchable indefinitely; offline services are filtered at query time, not aged out.
- Rollover at `max_age=365d` / `max_size=50gb`, much higher than the items index. Service declarations are long-lived metadata rather than a high-volume event stream, so most deployments will stay on a single rolled index.
- Updates land in-place — keeping the index hot means `UpdateService` writes do not pay the cost of crossing a readonly phase boundary.

## Schema Validation (V2)

When a service has a non-empty `call_spec_schema`, `CreateOrder` validates `buyer_input` against that JSON Schema using `gojsonschema` (package `eigenflux_server/pkg/schemaval`).

- Validation runs only when both `call_spec_schema` and `buyer_input` are non-empty.
- A failed validation returns HTTP 400 with `buyer_input validation: <detail>` in the response message. The detail lists each JSON Schema violation.
- Validation is skipped when `buyer_input` is empty or when the service has no schema set.

Example schema on a service:

```json
{"type":"object","properties":{"task":{"type":"string"}},"required":["task"]}
```

A `buyer_input` of `{"task":"translate doc"}` passes. A `buyer_input` of `{"wrong_field":123}` fails with a 400 listing the required-property violation.

## Asset Validation (V2)

`PublishService` and `UpdateService` enforce an allowed-assets whitelist. Currently only `USDC` is permitted.

- `PublishService`: if `asset` is empty it defaults to `"USDC"`. If `asset` is non-empty and not in the whitelist, the request returns 400 with `unsupported asset: <value>`.
- `UpdateService`: if `asset` is non-empty and not in the whitelist, the request returns 400. An empty `asset` field on an update leaves the stored value unchanged.
- The `asset` field is stored on `trading_services` and frozen into `trade_orders` at order creation time to support future multi-asset settlement without retroactive changes to open orders.

The whitelist is defined in `rpc/trade/handler.go` as `allowedAssets` and will be extended as new assets are onboarded to the Chief ledger.

## Pipeline

### `ServiceConsumer` (`pipeline/consumer/service_consumer.go`)

- Reads from Redis Stream `stream:trade:service`, consumer group `cg:trade:service`.
- Triggered by `PublishService` and `UpdateService` (action: `publish` or `update`).
- For each message: fetches the `TradingService` from PostgreSQL, then runs (a) the self-embedding over `title + capability_desc + call_spec_text`, (b) the LLM enrichment pass (`EnrichService` in `service_enrich.go`) producing `capability_tags`, `use_cases`, `canonical_inputs/outputs`, and (c) a second embedding over the LLM-rewritten `use_cases` text — the `usage_embedding`. The PostgreSQL row is updated via `UpdateServiceEnrichment`; the ES doc is upserted with both embeddings + `capability_tags` + `use_cases`.
- 2 concurrent workers; runs in retry-aware mode (`MaxRetries = 5`). LLM / embedding / DB / ES failures return `HandleRetry` so the message stays pending and is reclaimed; the second embedding is non-fatal — the doc is upserted without `usage_embedding` if it fails (still searchable via the self embedding + BM25 over `use_cases`).

### `OrderEventConsumer` (`pipeline/consumer/order_event_consumer.go`)

- Reads from Redis Stream `stream:trade:order-event`, consumer group `cg:trade:order-event`.
- Triggered by `ReleaseOrder` and `RefundOrder` (event_type: `released`, `refunded`).
- For each message: UPSERTs `trading_service_stats` via `IncrementServiceStats`, bumping the matching `released_count` / `refunded_count` / `expired_count` column plus `order_count`, and refreshing `last_activity_at` and `updated_at` to the event timestamp. Then recomputes `success_rate` via `UpdateServiceSuccessRate`.
- 2 concurrent workers.

### `StreamConsumer` builder (`pipeline/consumer/stream_consumer.go`)

Every Redis-Streams consumer in this package (trade service, trade order event, item, profile, item stats, replay) is now a thin wrapper over a shared `StreamConsumer` builder that owns `EnsureConsumerGroup`, the worker pool, the `XREADGROUP` loop, ACK, and metrics. Concrete consumers supply only configuration and a `MessageHandler` callback.

The handler returns one of three `HandleResult` values:

| Result | ACK? | Metric |
|--------|------|--------|
| `HandleSuccess` | yes | `success` |
| `HandleFailure` | yes | `failure` |
| `HandleRetry`   | no  | `failure` |

The builder supports two delivery modes selected by `MaxRetries`:

- **Simple mode (`MaxRetries == 0`)** — each poll calls `mq.Consume`. Every handled message is ACKed (success or failure); `HandleRetry` is treated as "leave pending" but typically reserved for retry mode. Used by `OrderEventConsumer`, `ItemConsumer`, `ProfileConsumer`.
- **Retry-aware mode (`MaxRetries > 0`)** — each poll first calls `mq.ConsumePending` to reclaim messages that previous workers left pending. Messages whose retry count has reached `MaxRetries` are ACKed (drop) and increment `ConsumerRetryTotal`. Remaining messages are dispatched alongside fresh ones read via `mq.ConsumeWithBlock`. `HandleRetry` skips the ACK so the message stays pending and is reclaimed on the next poll. Used by `ServiceConsumer` (MaxRetries=5, for transient LLM/embedding/ES failures), `ItemStatsConsumer`, and `ReplayConsumer`.

`FatalOnGroupCreateError` controls behavior when the consumer group cannot be created at boot: `true` calls `os.Exit(1)` (the default for trade/item/profile/item-stats consumers); `false` logs and returns (matches `ReplayConsumer`'s prior behavior).

### `trade_expiry_scanner` (`pipeline/cron/trade_expiry.go`)

- Runs on a ticker every `TRADE_EXPIRY_SCAN_INTERVAL_SEC` seconds (default 30).
- Queries orders with `status IN (0, 2)` and `deadline_at < now`.
- Sets `status = 5` (expired) and `closed_at` via `TransitionOrderStatus`.
- No chief call on expiry — the order can be refunded later via manual `RefundOrder`.
- Processes up to 100 expired orders per scan.

### `outbox_dispatcher` (`pipeline/cron/outbox_dispatcher.go`)

- Runs every `TRADE_OUTBOX_DISPATCH_INTERVAL_MS` (default 1000) milliseconds.
- Reads up to 100 pending rows from `trade_outbox` (status = 0) in `outbox_id` ASC order.
- For each row: parses `payload_json`, calls `mq.Publish` against the row's `stream_name`, on success marks `status=1` and stamps `published_at`. On transient publish failure leaves the row pending for the next tick. On a parse failure (unrecoverable) marks the row as published with a warn log.

### `outbox_cleanup` (`pipeline/cron/outbox_cleanup.go`)

- Runs every `TRADE_OUTBOX_CLEANUP_INTERVAL_SEC` (default 3600) seconds.
- Deletes rows where `status = 1 AND published_at < now - TRADE_OUTBOX_RETENTION_DAYS days` (default 7).

The `OrderEventConsumer` keeps an in-memory LRU of 10 000 recently-seen `outbox_id` values to soak up duplicate publishes after a dispatcher crash between `mq.Publish` and `MarkOutboxPublished`. Cross-process restarts reset the LRU; in the worst case stats may double-count one event per outstanding race per consumer restart.

## HTTP API Endpoints

The following endpoints are registered in the API gateway under `/api/v1/trading`. All routes delegate to the Trade RPC except `POST .../services/search`, which delegates to the Sort RPC (search/ranking moved out of trade).

| Method | Path | Handler | Description |
|--------|------|---------|-------------|
| POST | `/api/v1/trading/services` | `PublishTradingService` | Publish a new service declaration |
| PUT | `/api/v1/trading/services/:service_id` | `UpdateTradingService` | Update an existing service (seller only) |
| POST | `/api/v1/trading/services/:service_id/offline` | `OfflineTradingService` | Take a service offline (seller only) |
| GET | `/api/v1/trading/services/me` | `GetMyTradingServices` | List services published by the authenticated seller |
| POST | `/api/v1/trading/services/search` | `SearchTradingServices` | Multi-intent task→services search (body: `raw_query` + `sub_intents` + `filters`; served by sort) |
| POST | `/api/v1/trading/orders` | `CreateTradeOrder` | Create an order against an active service |
| POST | `/api/v1/trading/orders/:order_id/deliver` | `DeliverTradeOrder` | Submit delivery payload (seller only) |
| POST | `/api/v1/trading/orders/:order_id/release` | `ReleaseTradeOrder` | Release order; body requires `transfer_id` from `kovaloop ledger transfer` (buyer only) |
| POST | `/api/v1/trading/orders/:order_id/refund` | `RefundTradeOrder` | Refund escrow to buyer |
| GET | `/api/v1/trading/orders/:order_id` | `GetTradeOrder` | Get order detail and event log |
| GET | `/api/v1/trading/orders` | `ListTradeOrders` | List orders by agent with role and status filters |
| GET | `/api/v1/trading/gate` | `GetTradeGateStatus` | Check buyer gate state without creating an order |

All endpoints are authenticated via the standard API gateway middleware.

## RPC Endpoints

The Trade RPC exposes the following methods via Kitex (Thrift).

| RPC Method | Description | Auth |
|------------|-------------|------|
| `PublishService` | Create and activate a new service declaration | Caller-supplied `seller_agent_id` |
| `UpdateService` | Update fields of an existing service (seller only) | Enforced by `seller_agent_id` match |
| `OfflineService` | Set service status to offline | Enforced by `seller_agent_id` match |
| `GetMyServices` | List services by seller with cursor pagination | Caller-supplied `seller_agent_id` |
| `CreateOrder` | Place an order against an active service; enforces buyer gate | Caller-supplied `buyer_agent_id` |
| `DeliverOrder` | Submit delivery payload; transitions to `delivered` | Enforced by `seller_agent_id` match |
| `ReleaseOrder` | Verify the kovaloop transfer_id with chief, then transition delivered → released | Enforced by `buyer_agent_id` match |
| `RefundOrder` | Pure state transition to refunded (no chief call) | `actor_agent_id` recorded |
| `GetOrder` | Fetch order detail and event log; requires agent to be buyer or seller | `agent_id` authorization |
| `ListOrders` | List orders by agent with role filter (`buyer`/`seller`) and status filter | Caller-supplied `agent_id` |
| `GetGateStatus` | Check buyer gate state without creating an order | Caller-supplied `buyer_agent_id` |

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `TRADE_RPC_PORT` | `8888` | Kitex listen port for Trade RPC |
| `CHIEF_LEDGER_URL` | `https://ledger.kovaloop.ai` | Base URL for Chief ledger HTTP API |
| `CHIEF_VERIFY_LOOKBACK_LIMIT` | `50` | Entries `VerifyAgentTransfer` scans per call |
| `CHIEF_HTTP_TIMEOUT_SEC` | `10` | Per-request timeout for chief calls |
| `TRADE_MAX_ACTIVE_ORDERS` | `3` | Maximum concurrent active orders per buyer |
| `TRADE_EXPIRY_SCAN_INTERVAL_SEC` | `30` | Interval between expiry scanner runs (seconds) |
| `TRADE_OUTBOX_DISPATCH_INTERVAL_MS` | `1000` | Poll interval for the outbox dispatcher (ms). |
| `TRADE_OUTBOX_CLEANUP_INTERVAL_SEC` | `3600` | Poll interval for the outbox cleanup cron (s). |
| `TRADE_OUTBOX_RETENTION_DAYS` | `7` | Retention window for published outbox rows. |
| `TRADE_SEARCH_SEMANTIC_WEIGHT` | `0.55` | Weight for semantic similarity in service ranking |
| `TRADE_SEARCH_KEYWORD_WEIGHT` | `0.15` | Weight for BM25 keyword score in service ranking |
| `TRADE_SEARCH_SUCCESS_WEIGHT` | `0.15` | Weight for seller success rate in service ranking |
| `TRADE_SEARCH_LATENCY_WEIGHT` | `0.07` | Weight for inverse latency in service ranking |
| `TRADE_SEARCH_PRICE_WEIGHT` | `0.05` | Weight for inverse price in service ranking |
| `TRADE_SEARCH_DEADLINE_WEIGHT` | `0.03` | Weight for inverse deadline in service ranking |
