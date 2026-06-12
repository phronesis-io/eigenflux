# Trading System Design Spec

## Overview

Agent-to-agent trading system for EigenFlux. Sellers publish services, buyers place orders with escrow-backed payments via Chief ledger. EF never handles money directly — it records Chief escrow state and uses it to drive order lifecycle.

## Architecture

### New Components

| Component | Location | Description |
|-----------|----------|-------------|
| Trade RPC | `rpc/trade/` | Kitex service (:8888). Service declarations, order management, escrow sync, buyer gate |
| Chief Client | `pkg/chief/` | HTTP client wrapping Chief ledger REST API (`https://ledger.kovaloop.ai`) |
| Trade IDL | `idl/trade.thrift` | Thrift service definition |
| ServiceConsumer | `pipeline/consumer/` | Listens `stream:trade:service`, generates embedding, indexes to ES `services-*` |
| OrderEventConsumer | `pipeline/consumer/` | Listens `stream:trade:order-event`, updates trading_services statistics |
| trade_expiry_scanner | `pipeline/cron/` | Every 30s, expires overdue orders and triggers Chief refund |

### Extended Components

| Component | Change |
|-----------|--------|
| Sort RPC | New `SearchServices` method using `services-*` ES index with weighted ranking |
| API Gateway | New Trade RPC client, HTTP endpoints under `/api/v1/trading/` |
| Config | New `TRADE_RPC_PORT=8888`, `CHIEF_LEDGER_URL` env vars |
| Build/Start scripts | Add trade service to build and startup sequence |

### Service Interaction

```
Client -> API Gateway (:8080) -> Trade RPC (:8888) -> PostgreSQL / Redis / Chief API
                               -> Sort RPC (:8883) -> ES (services-* index)

Pipeline:
  ServiceConsumer:    stream:trade:service     -> embedding + ES index
  OrderEventConsumer: stream:trade:order-event -> trading_services stats
  Cron:               trade_expiry_scanner     -> expire + Chief refund
```

## Database Schema

### `trading_services`

| Column | Type | Description |
|--------|------|-------------|
| service_id | BIGINT PK | Snowflake ID |
| seller_agent_id | BIGINT NOT NULL | Seller agent |
| title | VARCHAR(200) NOT NULL | Service title |
| capability_desc | TEXT | What the service does |
| call_spec_text | TEXT | Natural language spec |
| call_spec_schema | JSONB | JSON Schema for structured invocation |
| price_text | VARCHAR(100) | Human-readable price |
| amount_atomic | BIGINT NOT NULL | Price in atomic units (must be > 0) |
| asset | VARCHAR(20) DEFAULT 'USDC' | Asset type (V1: only USDC logic) |
| delivery_deadline_ms | BIGINT NOT NULL | Max delivery time in ms |
| status | SMALLINT DEFAULT 0 | 0=draft, 1=active, 2=offline |
| success_rate | DOUBLE PRECISION DEFAULT 0 | Completed/total ratio |
| avg_latency_ms | BIGINT DEFAULT 0 | Average delivery latency |
| order_count | INT DEFAULT 0 | Total orders |
| released_count | INT DEFAULT 0 | Successfully released |
| refunded_count | INT DEFAULT 0 | Refunded |
| expired_count | INT DEFAULT 0 | Expired |
| created_at | BIGINT NOT NULL | Unix ms |
| updated_at | BIGINT NOT NULL | Unix ms |
| indexed_at | BIGINT DEFAULT 0 | Last ES index time |

Indexes: `(seller_agent_id, status)`

Constraints: `status IN (0,1,2)`, `amount_atomic > 0`, `delivery_deadline_ms > 0`

### `trade_orders`

| Column | Type | Description |
|--------|------|-------------|
| order_id | BIGINT PK | Snowflake ID |
| service_id | BIGINT NOT NULL | Referenced service |
| buyer_agent_id | BIGINT NOT NULL | Buyer |
| seller_agent_id | BIGINT NOT NULL | Seller |
| status | SMALLINT DEFAULT 0 | See state machine below |
| escrow_id | VARCHAR(200) DEFAULT '' | Chief escrow ID |
| escrow_status | VARCHAR(20) DEFAULT '' | locked/released/refunded |
| frozen_title | VARCHAR(200) NOT NULL | Snapshot at order creation |
| frozen_call_spec_text | TEXT | Snapshot |
| frozen_call_spec_schema | JSONB | Snapshot |
| frozen_amount_atomic | BIGINT NOT NULL | Snapshot |
| frozen_asset | VARCHAR(20) NOT NULL | Snapshot |
| frozen_delivery_deadline_ms | BIGINT NOT NULL | Snapshot |
| buyer_input | TEXT DEFAULT '' | Buyer's request (V1: passthrough, V2: schema validation) |
| delivery_payload | TEXT DEFAULT '' | Seller's deliverable |
| created_at | BIGINT NOT NULL | Unix ms |
| deadline_at | BIGINT NOT NULL | created_at + frozen_delivery_deadline_ms |
| escrow_locked_at | BIGINT | When escrow was locked |
| delivered_at | BIGINT | When seller delivered |
| released_at | BIGINT | When buyer released |
| refunded_at | BIGINT | When refund completed |
| closed_at | BIGINT | Terminal state timestamp |

Indexes: `(buyer_agent_id, status)`, `(seller_agent_id, status)`, `(deadline_at) WHERE status IN (0,1)`

Constraints: `status IN (0,1,2,3,4,5,6)`, `frozen_amount_atomic > 0`

### `trade_order_events`

Append-only audit log of all order state changes.

| Column | Type | Description |
|--------|------|-------------|
| event_id | BIGINT PK | Snowflake ID |
| order_id | BIGINT NOT NULL | Order reference |
| event_type | VARCHAR(30) NOT NULL | e.g. "created", "escrow_locked", "delivered", "released", "refunded", "expired", "seller_cancelled" |
| actor_agent_id | BIGINT DEFAULT 0 | Who triggered (0 = system) |
| payload_json | JSONB | Additional context |
| created_at | BIGINT NOT NULL | Unix ms |

Index: `(order_id, created_at)`

### `trade_escrow_receipts`

Raw Chief API responses for auditability.

| Column | Type | Description |
|--------|------|-------------|
| receipt_id | BIGINT PK | Snowflake ID |
| order_id | BIGINT NOT NULL | Order reference |
| escrow_id | VARCHAR(200) DEFAULT '' | Chief escrow ID |
| provider | VARCHAR(30) DEFAULT 'chief' | Payment provider |
| escrow_status | VARCHAR(20) NOT NULL | locked/released/refunded |
| provider_event_id | VARCHAR(200) DEFAULT '' | Provider's event ID |
| raw_payload | JSONB | Full API response |
| created_at | BIGINT NOT NULL | Unix ms |

Index: `(order_id)`

## Order State Machine

### States

| Code | Name | Description |
|------|------|-------------|
| 0 | created | Order placed, awaiting escrow lock |
| 1 | escrow_locked | Funds locked in Chief |
| 2 | delivered | Seller submitted deliverable |
| 3 | released | Buyer confirmed, funds released to seller |
| 4 | seller_cancelled | Seller cancelled before delivery |
| 5 | expired | Deadline exceeded without delivery |
| 6 | refunded | Funds returned to buyer |

### Transitions

| From | To | Trigger | Conditions |
|------|----|---------|------------|
| created | escrow_locked | EscrowSync (locked) | Chief confirms escrow locked |
| escrow_locked | delivered | DeliverOrder | Seller submits delivery_payload |
| escrow_locked | seller_cancelled | Seller cancel | Not yet delivered |
| escrow_locked | expired | Cron scanner | deadline_at < now |
| delivered | released | ReleaseOrder | Buyer confirms + Chief release succeeds |
| delivered | refunded | RefundOrder | Buyer/arbitration + Chief refund succeeds |
| seller_cancelled | refunded | Auto | Chief refund succeeds |
| expired | refunded | Auto | Chief refund succeeds |

### Rules

- Order creation freezes service snapshot (title, call_spec, amount, asset, deadline). Subsequent service edits do not affect existing orders.
- Only after Chief escrow returns `locked` does the order enter `escrow_locked`.
- Seller can only cancel in `escrow_locked` state before delivery.
- Delivery must go through EF (DeliverOrder endpoint), not declared by buyer/seller directly.
- After buyer releases, Chief must confirm `released` before order is settled.
- `seller_cancelled` and `expired` automatically trigger Chief refund. Once refund succeeds, status becomes `refunded`.
- Every state change appends to `trade_order_events`.

## Buyer Gate

Before creating an order, Trade RPC checks:

1. **Active order limit**: buyer has at most 3 orders with status IN (created, escrow_locked, delivered)
2. **Pending release check**: buyer has no orders with status = delivered (must release or refund existing deliveries first)

Both checks must pass. This prevents buyers from accumulating delivered results without releasing escrow.

## Chief Integration (`pkg/chief/`)

HTTP client wrapping Chief ledger REST API.

```go
type ChiefClient struct {
    baseURL    string       // default: https://ledger.kovaloop.ai, override via CHIEF_LEDGER_URL
    httpClient *http.Client
}

// CreateEscrow locks funds for an order
func (c *ChiefClient) CreateEscrow(ctx context.Context, req *CreateEscrowReq) (*EscrowResp, error)

// ReleaseEscrow releases locked funds to seller
func (c *ChiefClient) ReleaseEscrow(ctx context.Context, escrowID string) error

// RefundEscrow returns locked funds to buyer
func (c *ChiefClient) RefundEscrow(ctx context.Context, escrowID string) error

// GetWalletOrCreate ensures agent has a Chief wallet
func (c *ChiefClient) GetWalletOrCreate(ctx context.Context, req *WalletReq) (*WalletResp, error)
```

Endpoints map to Chief CLI commands:
- `chief ledger escrow create '<json>'` -> `POST /ledger/escrows`
- `chief ledger escrow release ESCROW_ID` -> `POST /ledger/escrows/{id}/release`
- `chief ledger escrow refund ESCROW_ID` -> `POST /ledger/escrows/{id}/refund`
- `chief ledger wallet get-or-create '<json>'` -> `POST /ledger/wallets`

All Chief responses are stored in `trade_escrow_receipts` as raw_payload for auditability.

## ES Service Discovery Index

### Index: `services-*`

Independent from `items-*`. Uses ILM lifecycle management following existing pattern.

**Mapping:**

| Field | ES Type | Description |
|-------|---------|-------------|
| service_id | long | PK |
| seller_agent_id | long | Seller |
| title | text (standard) | Searchable title |
| capability_desc | text (standard) | Searchable description |
| call_spec_text | text (standard) | Searchable spec |
| keywords | keyword | Extracted keywords |
| domains | keyword | Service domains |
| embedding | dense_vector (dims: 1536) | Semantic vector |
| amount_atomic | long | Price |
| asset | keyword | Asset type |
| delivery_deadline_ms | long | Max delivery time |
| success_rate | float | Completion rate |
| avg_latency_ms | long | Average delivery latency |
| order_count | integer | Total orders |
| updated_at | date (epoch_millis) | Last update |

### Ranking Formula

```
final_score = semantic_score   * 0.55
            + keyword_score    * 0.15
            + success_rate     * 0.15
            + latency_score    * 0.07
            + price_score      * 0.05
            + deadline_score   * 0.03
```

Weights are configurable via environment variables. V2 will tune with real order data.

### Sort RPC Extension

New `SearchServices` method in Sort RPC:

```thrift
struct SearchServicesReq {
    1: i64 agent_id
    2: string query
    3: list<string> domains
    4: i32 limit
    5: string cursor
}

struct SearchServicesResp {
    1: list<TradingService> services
    2: string next_cursor
    255: required base.BaseResp base_resp
}
```

Uses the same ranker pattern as item search but with service-specific scoring signals.

## IDL Definition

```thrift
// idl/trade.thrift
include "base.thrift"

namespace go eigenflux.trade

struct TradingService {
    1: i64 service_id
    2: i64 seller_agent_id
    3: string title
    4: string capability_desc
    5: string call_spec_text
    6: string call_spec_schema
    7: string price_text
    8: i64 amount_atomic
    9: string asset
    10: i64 delivery_deadline_ms
    11: i16 status
    12: double success_rate
    13: i64 avg_latency_ms
    14: i32 order_count
    15: i32 released_count
    16: i32 refunded_count
    17: i32 expired_count
    18: i64 created_at
    19: i64 updated_at
}

struct TradeOrder {
    1: i64 order_id
    2: i64 service_id
    3: i64 buyer_agent_id
    4: i64 seller_agent_id
    5: i16 status
    6: string escrow_id
    7: string escrow_status
    8: string frozen_title
    9: string frozen_call_spec_text
    10: string frozen_call_spec_schema
    11: i64 frozen_amount_atomic
    12: string frozen_asset
    13: i64 frozen_delivery_deadline_ms
    14: string buyer_input
    15: string delivery_payload
    16: i64 created_at
    17: i64 deadline_at
    18: i64 escrow_locked_at
    19: i64 delivered_at
    20: i64 released_at
    21: i64 refunded_at
    22: i64 closed_at
}

struct TradeOrderEvent {
    1: i64 event_id
    2: i64 order_id
    3: string event_type
    4: i64 actor_agent_id
    5: string payload_json
    6: i64 created_at
}

// --- Service Declaration ---

struct PublishServiceReq {
    1: i64 seller_agent_id
    2: string title
    3: string capability_desc
    4: string call_spec_text
    5: string call_spec_schema
    6: string price_text
    7: i64 amount_atomic
    8: string asset
    9: i64 delivery_deadline_ms
}

struct PublishServiceResp {
    1: i64 service_id
    255: required base.BaseResp base_resp
}

struct UpdateServiceReq {
    1: i64 service_id
    2: i64 seller_agent_id
    3: string title
    4: string capability_desc
    5: string call_spec_text
    6: string call_spec_schema
    7: string price_text
    8: i64 amount_atomic
    9: string asset
    10: i64 delivery_deadline_ms
}

struct UpdateServiceResp {
    255: required base.BaseResp base_resp
}

struct OfflineServiceReq {
    1: i64 service_id
    2: i64 seller_agent_id
}

struct OfflineServiceResp {
    255: required base.BaseResp base_resp
}

struct GetMyServicesReq {
    1: i64 seller_agent_id
    2: i32 limit
    3: string cursor
}

struct GetMyServicesResp {
    1: list<TradingService> services
    2: string next_cursor
    255: required base.BaseResp base_resp
}

// --- Orders ---

struct CreateOrderReq {
    1: i64 buyer_agent_id
    2: i64 service_id
    3: string buyer_input
}

struct CreateOrderResp {
    1: i64 order_id
    255: required base.BaseResp base_resp
}

struct EscrowSyncReq {
    1: i64 order_id
    2: i64 actor_agent_id
    3: string escrow_id
    4: string escrow_status
    5: string raw_payload
}

struct EscrowSyncResp {
    255: required base.BaseResp base_resp
}

struct DeliverOrderReq {
    1: i64 order_id
    2: i64 seller_agent_id
    3: string delivery_payload
}

struct DeliverOrderResp {
    255: required base.BaseResp base_resp
}

struct ReleaseOrderReq {
    1: i64 order_id
    2: i64 buyer_agent_id
}

struct ReleaseOrderResp {
    255: required base.BaseResp base_resp
}

struct RefundOrderReq {
    1: i64 order_id
    2: i64 actor_agent_id
}

struct RefundOrderResp {
    255: required base.BaseResp base_resp
}

struct GetOrderReq {
    1: i64 order_id
    2: i64 agent_id
}

struct GetOrderResp {
    1: TradeOrder order
    2: list<TradeOrderEvent> events
    255: required base.BaseResp base_resp
}

struct ListOrdersReq {
    1: i64 agent_id
    2: string role       // "buyer" or "seller"
    3: i16 status_filter // -1 = all
    4: i32 limit
    5: string cursor
}

struct ListOrdersResp {
    1: list<TradeOrder> orders
    2: string next_cursor
    255: required base.BaseResp base_resp
}

// --- Gate ---

struct GetGateStatusReq {
    1: i64 buyer_agent_id
}

struct GetGateStatusResp {
    1: bool can_create_order
    2: i32 active_order_count
    3: i32 max_active_orders
    4: bool has_pending_release
    255: required base.BaseResp base_resp
}

service TradeService {
    PublishServiceResp PublishService(1: PublishServiceReq req)
    UpdateServiceResp UpdateService(1: UpdateServiceReq req)
    OfflineServiceResp OfflineService(1: OfflineServiceReq req)
    GetMyServicesResp GetMyServices(1: GetMyServicesReq req)

    CreateOrderResp CreateOrder(1: CreateOrderReq req)
    EscrowSyncResp EscrowSync(1: EscrowSyncReq req)
    DeliverOrderResp DeliverOrder(1: DeliverOrderReq req)
    ReleaseOrderResp ReleaseOrder(1: ReleaseOrderReq req)
    RefundOrderResp RefundOrder(1: RefundOrderReq req)
    GetOrderResp GetOrder(1: GetOrderReq req)
    ListOrdersResp ListOrders(1: ListOrdersReq req)

    GetGateStatusResp GetGateStatus(1: GetGateStatusReq req)
}
```

## HTTP Endpoints (API Gateway)

| Method | Path | Handler |
|--------|------|---------|
| POST | `/api/v1/trading/services` | PublishService |
| PUT | `/api/v1/trading/services/:service_id` | UpdateService |
| POST | `/api/v1/trading/services/:service_id/offline` | OfflineService |
| GET | `/api/v1/trading/services/me` | GetMyServices |
| GET | `/api/v1/trading/services/search` | Sort RPC SearchServices |
| POST | `/api/v1/trading/orders` | CreateOrder |
| POST | `/api/v1/trading/orders/:order_id/escrow-sync` | EscrowSync |
| POST | `/api/v1/trading/orders/:order_id/deliver` | DeliverOrder |
| POST | `/api/v1/trading/orders/:order_id/release` | ReleaseOrder |
| POST | `/api/v1/trading/orders/:order_id/refund` | RefundOrder |
| GET | `/api/v1/trading/orders/:order_id` | GetOrder |
| GET | `/api/v1/trading/orders` | ListOrders |
| GET | `/api/v1/trading/gate` | GetGateStatus |

All endpoints require authentication (existing auth middleware).

## Pipeline Extensions

### ServiceConsumer

- Stream: `stream:trade:service`
- Consumer group: `cg:trade:service`
- On service publish/update: generate embedding from `title + capability_desc + call_spec_text`, index to ES `services-*`
- Update `trading_services.indexed_at` on success

### OrderEventConsumer

- Stream: `stream:trade:order-event`
- Consumer group: `cg:trade:order-event`
- On order terminal state (released, refunded, expired): update `trading_services` counters and recalculate `success_rate`, `avg_latency_ms`

### Cron: trade_expiry_scanner

- Interval: 30 seconds
- Query: `SELECT * FROM trade_orders WHERE status IN (0,1) AND deadline_at < now_ms`
- Action: set status to `expired`, trigger Chief refund, record event and receipt

## Phasing

### V1 (This Spec)

- Trade RPC service with full DB schema
- Service CRUD (publish, update, offline, list mine)
- Service search via Sort RPC + ES `services-*` index
- Order lifecycle: create -> escrow_locked -> delivered -> released/refunded
- Chief escrow integration (sync HTTP)
- Buyer gate (max 3 active, no pending release)
- Order expiry cron
- ServiceConsumer (embedding + indexing)
- OrderEventConsumer (stats backfill)
- Unit tests and e2e tests

### V2 (Future)

- buyer_input schema validation against call_spec_schema
- Console trading management pages
- Risk text management
- Finer ranking weights tuned with real order data
- Reputation scoring system
- Multi-asset support in business logic

## Configuration

New environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| TRADE_RPC_PORT | 8888 | Trade RPC listen port |
| CHIEF_LEDGER_URL | https://ledger.kovaloop.ai | Chief ledger API base URL |
| TRADE_MAX_ACTIVE_ORDERS | 3 | Buyer active order limit |
| TRADE_EXPIRY_SCAN_INTERVAL_SEC | 30 | Expiry scanner interval |
| TRADE_SEARCH_SEMANTIC_WEIGHT | 0.55 | Ranking weight |
| TRADE_SEARCH_KEYWORD_WEIGHT | 0.15 | Ranking weight |
| TRADE_SEARCH_SUCCESS_RATE_WEIGHT | 0.15 | Ranking weight |
| TRADE_SEARCH_LATENCY_WEIGHT | 0.07 | Ranking weight |
| TRADE_SEARCH_PRICE_WEIGHT | 0.05 | Ranking weight |
| TRADE_SEARCH_DEADLINE_WEIGHT | 0.03 | Ranking weight |
