# Trading System Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add agent-to-agent trading with escrow-backed payments via Chief ledger to EigenFlux.

**Architecture:** New Trade RPC (Kitex :8888) owns service declarations, orders, escrow sync, buyer gate. Sort RPC gains `SearchServices` over a new `services-*` ES index. Pipeline adds consumers for embedding/indexing and stats backfill, plus a cron job for order expiry.

**Tech Stack:** Go 1.25, Kitex, Hertz, GORM, PostgreSQL, Redis Streams, Elasticsearch 8, Chief ledger REST API

**Spec:** `docs/superpowers/specs/2026-06-03-trading-system-design.md`

---

## File Structure

### New Files

| File | Responsibility |
|------|----------------|
| `idl/trade.thrift` | Thrift service definition for TradeService |
| `migrations/000014_add_trading.sql` | DB schema: trading_services, trade_orders, trade_order_events, trade_escrow_receipts |
| `rpc/trade/main.go` | Trade RPC service entry point |
| `rpc/trade/handler.go` | TradeService RPC method implementations |
| `rpc/trade/dal/db.go` | GORM models and DAL functions for trading tables |
| `rpc/trade/dal/es.go` | ES indexing and search for `services-*` index |
| `rpc/trade/dal/es_query.go` | ES query builder for service search |
| `rpc/trade/gate.go` | Buyer gate logic (active order limit, pending release check) |
| `rpc/trade/statemachine.go` | Order state machine transitions and validation |
| `pkg/chief/client.go` | HTTP client for Chief ledger API (escrow create/release/refund, wallet) |
| `pkg/chief/types.go` | Request/response types for Chief API |
| `pkg/es/services_ilm.go` | ILM policy, index template, bootstrap for `services-*` index |
| `pkg/es/services_mapping.go` | ES mapping for service documents |
| `pipeline/consumer/service_consumer.go` | Consumes `stream:trade:service`, generates embedding, indexes to ES |
| `pipeline/consumer/order_event_consumer.go` | Consumes `stream:trade:order-event`, updates trading_services stats |
| `pipeline/cron/trade_expiry.go` | Scans overdue orders, triggers expiry + Chief refund |
| `tests/trade/trade_test.go` | E2E tests for trading system |

### Modified Files

| File | Change |
|------|--------|
| `pkg/config/config.go` | Add `TradeRPCPort`, `ChiefLedgerURL`, `TradeMaxActiveOrders`, `TradeExpiryScanIntervalSec`, trade search weight fields |
| `pkg/es/client.go` | Call `SetupServicesILM()` during `InitES()` |
| `api/clients/clients.go` | Add `TradeClient` variable |
| `api/main.go` | Init Trade RPC client, wire to `clients.TradeClient` |
| `scripts/common/build.sh` | Add `"trade:./rpc/trade/"` to `ALL_SERVICES` |
| `scripts/local/start_local.sh` | Add `TRADE_RPC_PORT`, add `trade` to `SERVICE_MAP` |
| `pipeline/main.go` | Start `ServiceConsumer` and `OrderEventConsumer` |
| `pipeline/cron/main.go` | Start `StartTradeExpiryScanner` |

---

## Task 1: Database Migration

**Files:**
- Create: `migrations/000014_add_trading.sql`

- [ ] **Step 1: Write migration file**

```sql
-- +goose Up

CREATE TABLE trading_services (
    service_id           BIGINT PRIMARY KEY,
    seller_agent_id      BIGINT NOT NULL,
    title                VARCHAR(200) NOT NULL,
    capability_desc      TEXT NOT NULL DEFAULT '',
    call_spec_text       TEXT NOT NULL DEFAULT '',
    call_spec_schema     JSONB,
    price_text           VARCHAR(100) NOT NULL DEFAULT '',
    amount_atomic        BIGINT NOT NULL,
    asset                VARCHAR(20) NOT NULL DEFAULT 'USDC',
    delivery_deadline_ms BIGINT NOT NULL,
    status               SMALLINT NOT NULL DEFAULT 0,
    success_rate         DOUBLE PRECISION NOT NULL DEFAULT 0,
    avg_latency_ms       BIGINT NOT NULL DEFAULT 0,
    order_count          INT NOT NULL DEFAULT 0,
    released_count       INT NOT NULL DEFAULT 0,
    refunded_count       INT NOT NULL DEFAULT 0,
    expired_count        INT NOT NULL DEFAULT 0,
    created_at           BIGINT NOT NULL,
    updated_at           BIGINT NOT NULL,
    indexed_at           BIGINT NOT NULL DEFAULT 0,

    CONSTRAINT chk_trading_services_status CHECK (status IN (0, 1, 2)),
    CONSTRAINT chk_trading_services_amount CHECK (amount_atomic > 0),
    CONSTRAINT chk_trading_services_deadline CHECK (delivery_deadline_ms > 0)
);
CREATE INDEX idx_trading_services_seller ON trading_services(seller_agent_id, status);

CREATE TABLE trade_orders (
    order_id                    BIGINT PRIMARY KEY,
    service_id                  BIGINT NOT NULL,
    buyer_agent_id              BIGINT NOT NULL,
    seller_agent_id             BIGINT NOT NULL,
    status                      SMALLINT NOT NULL DEFAULT 0,
    escrow_id                   VARCHAR(200) NOT NULL DEFAULT '',
    escrow_status               VARCHAR(20) NOT NULL DEFAULT '',
    frozen_title                VARCHAR(200) NOT NULL,
    frozen_call_spec_text       TEXT NOT NULL DEFAULT '',
    frozen_call_spec_schema     JSONB,
    frozen_amount_atomic        BIGINT NOT NULL,
    frozen_asset                VARCHAR(20) NOT NULL,
    frozen_delivery_deadline_ms BIGINT NOT NULL,
    buyer_input                 TEXT NOT NULL DEFAULT '',
    delivery_payload            TEXT NOT NULL DEFAULT '',
    created_at                  BIGINT NOT NULL,
    deadline_at                 BIGINT NOT NULL,
    escrow_locked_at            BIGINT,
    delivered_at                BIGINT,
    released_at                 BIGINT,
    refunded_at                 BIGINT,
    closed_at                   BIGINT,

    CONSTRAINT chk_trade_orders_status CHECK (status IN (0,1,2,3,4,5,6)),
    CONSTRAINT chk_trade_orders_amount CHECK (frozen_amount_atomic > 0)
);
CREATE INDEX idx_trade_orders_buyer ON trade_orders(buyer_agent_id, status);
CREATE INDEX idx_trade_orders_seller ON trade_orders(seller_agent_id, status);
CREATE INDEX idx_trade_orders_deadline ON trade_orders(deadline_at) WHERE status IN (0, 1);

CREATE TABLE trade_order_events (
    event_id       BIGINT PRIMARY KEY,
    order_id       BIGINT NOT NULL,
    event_type     VARCHAR(30) NOT NULL,
    actor_agent_id BIGINT NOT NULL DEFAULT 0,
    payload_json   JSONB,
    created_at     BIGINT NOT NULL
);
CREATE INDEX idx_trade_order_events_order ON trade_order_events(order_id, created_at);

CREATE TABLE trade_escrow_receipts (
    receipt_id        BIGINT PRIMARY KEY,
    order_id          BIGINT NOT NULL,
    escrow_id         VARCHAR(200) NOT NULL DEFAULT '',
    provider          VARCHAR(30) NOT NULL DEFAULT 'chief',
    escrow_status     VARCHAR(20) NOT NULL,
    provider_event_id VARCHAR(200) NOT NULL DEFAULT '',
    raw_payload       JSONB,
    created_at        BIGINT NOT NULL
);
CREATE INDEX idx_trade_escrow_receipts_order ON trade_escrow_receipts(order_id);

-- +goose Down
DROP TABLE IF EXISTS trade_escrow_receipts;
DROP TABLE IF EXISTS trade_order_events;
DROP TABLE IF EXISTS trade_orders;
DROP TABLE IF EXISTS trading_services;
```

- [ ] **Step 2: Run migration**

Run: `bash scripts/common/migrate_up.sh`
Expected: migration 000014 applied successfully

- [ ] **Step 3: Verify tables exist**

Run: `docker compose -p myhub exec -T postgres psql -U eigenflux -d eigenflux -c "\dt trading_services; \dt trade_orders; \dt trade_order_events; \dt trade_escrow_receipts;"`
Expected: all 4 tables listed

- [ ] **Step 4: Commit**

```bash
git add migrations/000014_add_trading.sql
git commit -m "feat(trade): add trading system database tables"
```

---

## Task 2: Config Extension

**Files:**
- Modify: `pkg/config/config.go`

- [ ] **Step 1: Add trade config fields to Config struct**

Add after `NotificationRPCPort int` (around line 47):

```go
TradeRPCPort int
```

Add after the `EnableReplayLog` field (around line 88):

```go
// Trade
ChiefLedgerURL              string
TradeMaxActiveOrders        int
TradeExpiryScanIntervalSec  int
TradeSearchSemanticWeight   float64
TradeSearchKeywordWeight    float64
TradeSearchSuccessWeight    float64
TradeSearchLatencyWeight    float64
TradeSearchPriceWeight      float64
TradeSearchDeadlineWeight   float64
```

- [ ] **Step 2: Add config loading in Load() function**

Add after the `NotificationRPCPort` line in `Load()` (around line 164):

```go
TradeRPCPort: getEnvInt("TRADE_RPC_PORT", 8888),
```

Add after the `EnableReplayLog` line (around line 206):

```go
ChiefLedgerURL:              getEnv("CHIEF_LEDGER_URL", "https://ledger.kovaloop.ai"),
TradeMaxActiveOrders:        getEnvInt("TRADE_MAX_ACTIVE_ORDERS", 3),
TradeExpiryScanIntervalSec:  getEnvInt("TRADE_EXPIRY_SCAN_INTERVAL_SEC", 30),
TradeSearchSemanticWeight:   getEnvFloat("TRADE_SEARCH_SEMANTIC_WEIGHT", 0.55),
TradeSearchKeywordWeight:    getEnvFloat("TRADE_SEARCH_KEYWORD_WEIGHT", 0.15),
TradeSearchSuccessWeight:    getEnvFloat("TRADE_SEARCH_SUCCESS_WEIGHT", 0.15),
TradeSearchLatencyWeight:    getEnvFloat("TRADE_SEARCH_LATENCY_WEIGHT", 0.07),
TradeSearchPriceWeight:      getEnvFloat("TRADE_SEARCH_PRICE_WEIGHT", 0.05),
TradeSearchDeadlineWeight:   getEnvFloat("TRADE_SEARCH_DEADLINE_WEIGHT", 0.03),
```

- [ ] **Step 3: Verify build**

Run: `cd /Users/misaki/go/eigenflux && go build ./pkg/config/`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add pkg/config/config.go
git commit -m "feat(trade): add trade config fields"
```

---

## Task 3: Thrift IDL + Code Generation

**Files:**
- Create: `idl/trade.thrift`

- [ ] **Step 1: Write trade.thrift**

```thrift
namespace go eigenflux.trade

include "base.thrift"

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
    2: string role
    3: i16 status_filter
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

// --- Search (delegated to Sort RPC, defined here for shared types) ---

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

    SearchServicesResp SearchServices(1: SearchServicesReq req)
}
```

- [ ] **Step 2: Generate Kitex code**

Run: `cd /Users/misaki/go/eigenflux && kitex -module eigenflux_server idl/trade.thrift`
Expected: generated code in `kitex_gen/eigenflux/trade/`

- [ ] **Step 3: Verify generated code compiles**

Run: `go build ./kitex_gen/...`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add idl/trade.thrift kitex_gen/eigenflux/trade/
git commit -m "feat(trade): add trade IDL and generated code"
```

---

## Task 4: Chief Client (`pkg/chief/`)

**Files:**
- Create: `pkg/chief/types.go`
- Create: `pkg/chief/client.go`

- [ ] **Step 1: Write types.go**

```go
package chief

type CreateEscrowReq struct {
	FromAgentID string `json:"fromAgentId"`
	ToAgentID   string `json:"toAgentId"`
	Amount      string `json:"amount"`
	Asset       string `json:"asset"`
	Reason      string `json:"reason"`
}

type EscrowResp struct {
	EscrowID string `json:"escrowId"`
	Status   string `json:"status"`
}

type WalletReq struct {
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	Email     string `json:"email"`
}

type WalletResp struct {
	WalletID string `json:"walletId"`
	AgentID  string `json:"agentId"`
}

type LedgerError struct {
	StatusCode int
	Detail     string `json:"detail"`
}

func (e *LedgerError) Error() string {
	return e.Detail
}
```

- [ ] **Step 2: Write client.go**

```go
package chief

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) CreateEscrow(ctx context.Context, req *CreateEscrowReq) (*EscrowResp, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal escrow request: %w", err)
	}
	var resp EscrowResp
	if err := c.doJSON(ctx, http.MethodPost, "/ledger/escrows", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) ReleaseEscrow(ctx context.Context, escrowID string) error {
	return c.doJSON(ctx, http.MethodPost, "/ledger/escrows/"+escrowID+"/release", nil, nil)
}

func (c *Client) RefundEscrow(ctx context.Context, escrowID string) error {
	return c.doJSON(ctx, http.MethodPost, "/ledger/escrows/"+escrowID+"/refund", nil, nil)
}

func (c *Client) GetWalletOrCreate(ctx context.Context, req *WalletReq) (*WalletResp, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal wallet request: %w", err)
	}
	var resp WalletResp
	if err := c.doJSON(ctx, http.MethodPost, "/ledger/wallets", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, reqBody []byte, result interface{}) error {
	var bodyReader io.Reader
	if reqBody != nil {
		bodyReader = bytes.NewReader(reqBody)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("chief request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		le := &LedgerError{StatusCode: resp.StatusCode, Detail: string(raw)}
		_ = json.Unmarshal(raw, le)
		return le
	}

	if result != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, result); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}
	return nil
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./pkg/chief/`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add pkg/chief/
git commit -m "feat(trade): add Chief ledger HTTP client"
```

---

## Task 5: Trade RPC DAL

**Files:**
- Create: `rpc/trade/dal/db.go`

- [ ] **Step 1: Write GORM models and DAL functions**

```go
package dal

import (
	"time"

	"gorm.io/gorm"
)

// Service status
const (
	ServiceStatusDraft   int16 = 0
	ServiceStatusActive  int16 = 1
	ServiceStatusOffline int16 = 2
)

// Order status
const (
	OrderStatusCreated         int16 = 0
	OrderStatusEscrowLocked    int16 = 1
	OrderStatusDelivered       int16 = 2
	OrderStatusReleased        int16 = 3
	OrderStatusSellerCancelled int16 = 4
	OrderStatusExpired         int16 = 5
	OrderStatusRefunded        int16 = 6
)

type TradingService struct {
	ServiceID          int64   `gorm:"column:service_id;primaryKey"`
	SellerAgentID      int64   `gorm:"column:seller_agent_id;not null"`
	Title              string  `gorm:"column:title;type:varchar(200);not null"`
	CapabilityDesc     string  `gorm:"column:capability_desc;type:text;not null;default:''"`
	CallSpecText       string  `gorm:"column:call_spec_text;type:text;not null;default:''"`
	CallSpecSchema     *string `gorm:"column:call_spec_schema;type:jsonb"`
	PriceText          string  `gorm:"column:price_text;type:varchar(100);not null;default:''"`
	AmountAtomic       int64   `gorm:"column:amount_atomic;not null"`
	Asset              string  `gorm:"column:asset;type:varchar(20);not null;default:'USDC'"`
	DeliveryDeadlineMs int64   `gorm:"column:delivery_deadline_ms;not null"`
	Status             int16   `gorm:"column:status;not null;default:0"`
	SuccessRate        float64 `gorm:"column:success_rate;not null;default:0"`
	AvgLatencyMs       int64   `gorm:"column:avg_latency_ms;not null;default:0"`
	OrderCount         int32   `gorm:"column:order_count;not null;default:0"`
	ReleasedCount      int32   `gorm:"column:released_count;not null;default:0"`
	RefundedCount      int32   `gorm:"column:refunded_count;not null;default:0"`
	ExpiredCount       int32   `gorm:"column:expired_count;not null;default:0"`
	CreatedAt          int64   `gorm:"column:created_at;not null"`
	UpdatedAt          int64   `gorm:"column:updated_at;not null"`
	IndexedAt          int64   `gorm:"column:indexed_at;not null;default:0"`
}

func (TradingService) TableName() string { return "trading_services" }

type TradeOrder struct {
	OrderID                  int64   `gorm:"column:order_id;primaryKey"`
	ServiceID                int64   `gorm:"column:service_id;not null"`
	BuyerAgentID             int64   `gorm:"column:buyer_agent_id;not null"`
	SellerAgentID            int64   `gorm:"column:seller_agent_id;not null"`
	Status                   int16   `gorm:"column:status;not null;default:0"`
	EscrowID                 string  `gorm:"column:escrow_id;type:varchar(200);not null;default:''"`
	EscrowStatus             string  `gorm:"column:escrow_status;type:varchar(20);not null;default:''"`
	FrozenTitle              string  `gorm:"column:frozen_title;type:varchar(200);not null"`
	FrozenCallSpecText       string  `gorm:"column:frozen_call_spec_text;type:text;not null;default:''"`
	FrozenCallSpecSchema     *string `gorm:"column:frozen_call_spec_schema;type:jsonb"`
	FrozenAmountAtomic       int64   `gorm:"column:frozen_amount_atomic;not null"`
	FrozenAsset              string  `gorm:"column:frozen_asset;type:varchar(20);not null"`
	FrozenDeliveryDeadlineMs int64   `gorm:"column:frozen_delivery_deadline_ms;not null"`
	BuyerInput               string  `gorm:"column:buyer_input;type:text;not null;default:''"`
	DeliveryPayload          string  `gorm:"column:delivery_payload;type:text;not null;default:''"`
	CreatedAt                int64   `gorm:"column:created_at;not null"`
	DeadlineAt               int64   `gorm:"column:deadline_at;not null"`
	EscrowLockedAt           *int64  `gorm:"column:escrow_locked_at"`
	DeliveredAt              *int64  `gorm:"column:delivered_at"`
	ReleasedAt               *int64  `gorm:"column:released_at"`
	RefundedAt               *int64  `gorm:"column:refunded_at"`
	ClosedAt                 *int64  `gorm:"column:closed_at"`
}

func (TradeOrder) TableName() string { return "trade_orders" }

type TradeOrderEvent struct {
	EventID      int64   `gorm:"column:event_id;primaryKey"`
	OrderID      int64   `gorm:"column:order_id;not null"`
	EventType    string  `gorm:"column:event_type;type:varchar(30);not null"`
	ActorAgentID int64   `gorm:"column:actor_agent_id;not null;default:0"`
	PayloadJSON  *string `gorm:"column:payload_json;type:jsonb"`
	CreatedAt    int64   `gorm:"column:created_at;not null"`
}

func (TradeOrderEvent) TableName() string { return "trade_order_events" }

type TradeEscrowReceipt struct {
	ReceiptID       int64   `gorm:"column:receipt_id;primaryKey"`
	OrderID         int64   `gorm:"column:order_id;not null"`
	EscrowID        string  `gorm:"column:escrow_id;type:varchar(200);not null;default:''"`
	Provider        string  `gorm:"column:provider;type:varchar(30);not null;default:'chief'"`
	EscrowStatus    string  `gorm:"column:escrow_status;type:varchar(20);not null"`
	ProviderEventID string  `gorm:"column:provider_event_id;type:varchar(200);not null;default:''"`
	RawPayload      *string `gorm:"column:raw_payload;type:jsonb"`
	CreatedAt       int64   `gorm:"column:created_at;not null"`
}

func (TradeEscrowReceipt) TableName() string { return "trade_escrow_receipts" }

// --- Service DAL ---

func CreateService(db *gorm.DB, svc *TradingService) error {
	now := time.Now().UnixMilli()
	svc.CreatedAt = now
	svc.UpdatedAt = now
	svc.Status = ServiceStatusActive
	return db.Create(svc).Error
}

func GetService(db *gorm.DB, serviceID int64) (*TradingService, error) {
	var svc TradingService
	if err := db.Where("service_id = ?", serviceID).First(&svc).Error; err != nil {
		return nil, err
	}
	return &svc, nil
}

func UpdateService(db *gorm.DB, serviceID, sellerAgentID int64, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now().UnixMilli()
	return db.Model(&TradingService{}).
		Where("service_id = ? AND seller_agent_id = ?", serviceID, sellerAgentID).
		Updates(updates).Error
}

func OfflineService(db *gorm.DB, serviceID, sellerAgentID int64) error {
	return UpdateService(db, serviceID, sellerAgentID, map[string]interface{}{
		"status": ServiceStatusOffline,
	})
}

func ListServicesBySeller(db *gorm.DB, sellerAgentID int64, limit int, cursor int64) ([]*TradingService, error) {
	var services []*TradingService
	q := db.Where("seller_agent_id = ?", sellerAgentID)
	if cursor > 0 {
		q = q.Where("service_id < ?", cursor)
	}
	if err := q.Order("service_id DESC").Limit(limit).Find(&services).Error; err != nil {
		return nil, err
	}
	return services, nil
}

// --- Order DAL ---

func CreateOrder(db *gorm.DB, order *TradeOrder) error {
	order.CreatedAt = time.Now().UnixMilli()
	order.DeadlineAt = order.CreatedAt + order.FrozenDeliveryDeadlineMs
	return db.Create(order).Error
}

func GetOrder(db *gorm.DB, orderID int64) (*TradeOrder, error) {
	var order TradeOrder
	if err := db.Where("order_id = ?", orderID).First(&order).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

func UpdateOrderStatus(db *gorm.DB, orderID int64, updates map[string]interface{}) error {
	return db.Model(&TradeOrder{}).Where("order_id = ?", orderID).Updates(updates).Error
}

func ListOrdersByAgent(db *gorm.DB, agentID int64, role string, statusFilter int16, limit int, cursor int64) ([]*TradeOrder, error) {
	var orders []*TradeOrder
	q := db.Model(&TradeOrder{})
	if role == "buyer" {
		q = q.Where("buyer_agent_id = ?", agentID)
	} else {
		q = q.Where("seller_agent_id = ?", agentID)
	}
	if statusFilter >= 0 {
		q = q.Where("status = ?", statusFilter)
	}
	if cursor > 0 {
		q = q.Where("order_id < ?", cursor)
	}
	if err := q.Order("order_id DESC").Limit(limit).Find(&orders).Error; err != nil {
		return nil, err
	}
	return orders, nil
}

func CountActiveOrders(db *gorm.DB, buyerAgentID int64) (int64, error) {
	var count int64
	err := db.Model(&TradeOrder{}).
		Where("buyer_agent_id = ? AND status IN ?", buyerAgentID, []int16{
			OrderStatusCreated, OrderStatusEscrowLocked, OrderStatusDelivered,
		}).Count(&count).Error
	return count, err
}

func HasPendingRelease(db *gorm.DB, buyerAgentID int64) (bool, error) {
	var count int64
	err := db.Model(&TradeOrder{}).
		Where("buyer_agent_id = ? AND status = ?", buyerAgentID, OrderStatusDelivered).
		Count(&count).Error
	return count > 0, err
}

func FindExpiredOrders(db *gorm.DB, nowMs int64, limit int) ([]*TradeOrder, error) {
	var orders []*TradeOrder
	err := db.Where("status IN ? AND deadline_at < ?",
		[]int16{OrderStatusCreated, OrderStatusEscrowLocked}, nowMs).
		Limit(limit).Find(&orders).Error
	return orders, err
}

// --- Event DAL ---

func CreateOrderEvent(db *gorm.DB, event *TradeOrderEvent) error {
	event.CreatedAt = time.Now().UnixMilli()
	return db.Create(event).Error
}

func ListOrderEvents(db *gorm.DB, orderID int64) ([]*TradeOrderEvent, error) {
	var events []*TradeOrderEvent
	err := db.Where("order_id = ?", orderID).Order("created_at ASC").Find(&events).Error
	return events, err
}

// --- Escrow Receipt DAL ---

func CreateEscrowReceipt(db *gorm.DB, receipt *TradeEscrowReceipt) error {
	receipt.CreatedAt = time.Now().UnixMilli()
	return db.Create(receipt).Error
}

// --- Stats Update ---

func IncrementServiceStats(db *gorm.DB, serviceID int64, column string) error {
	return db.Model(&TradingService{}).
		Where("service_id = ?", serviceID).
		UpdateColumn(column, gorm.Expr(column+" + 1")).
		UpdateColumn("order_count", gorm.Expr("order_count + 1")).
		UpdateColumn("updated_at", time.Now().UnixMilli()).Error
}

func UpdateServiceSuccessRate(db *gorm.DB, serviceID int64) error {
	return db.Exec(`
		UPDATE trading_services
		SET success_rate = CASE WHEN order_count > 0
			THEN released_count::float / order_count
			ELSE 0 END,
		    updated_at = ?
		WHERE service_id = ?
	`, time.Now().UnixMilli(), serviceID).Error
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./rpc/trade/dal/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add rpc/trade/dal/db.go
git commit -m "feat(trade): add Trade DAL with GORM models"
```

---

## Task 6: Order State Machine

**Files:**
- Create: `rpc/trade/statemachine.go`

- [ ] **Step 1: Write state machine**

```go
package main

import (
	"fmt"

	"eigenflux_server/rpc/trade/dal"
)

// validTransitions maps current status -> set of allowed next statuses.
var validTransitions = map[int16][]int16{
	dal.OrderStatusCreated:         {dal.OrderStatusEscrowLocked},
	dal.OrderStatusEscrowLocked:    {dal.OrderStatusDelivered, dal.OrderStatusSellerCancelled, dal.OrderStatusExpired},
	dal.OrderStatusDelivered:       {dal.OrderStatusReleased, dal.OrderStatusRefunded},
	dal.OrderStatusSellerCancelled: {dal.OrderStatusRefunded},
	dal.OrderStatusExpired:         {dal.OrderStatusRefunded},
}

func validateTransition(from, to int16) error {
	allowed, ok := validTransitions[from]
	if !ok {
		return fmt.Errorf("no transitions from status %d", from)
	}
	for _, a := range allowed {
		if a == to {
			return nil
		}
	}
	return fmt.Errorf("invalid transition: %d -> %d", from, to)
}

func isTerminalStatus(status int16) bool {
	return status == dal.OrderStatusReleased ||
		status == dal.OrderStatusRefunded
}

func isActiveStatus(status int16) bool {
	return status == dal.OrderStatusCreated ||
		status == dal.OrderStatusEscrowLocked ||
		status == dal.OrderStatusDelivered
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./rpc/trade/`
Expected: may fail (handler.go not yet created), that's fine — just check statemachine.go compiles with `go vet ./rpc/trade/statemachine.go`

- [ ] **Step 3: Commit**

```bash
git add rpc/trade/statemachine.go
git commit -m "feat(trade): add order state machine"
```

---

## Task 7: Buyer Gate

**Files:**
- Create: `rpc/trade/gate.go`

- [ ] **Step 1: Write gate logic**

```go
package main

import (
	"eigenflux_server/rpc/trade/dal"
	"gorm.io/gorm"
)

type GateResult struct {
	CanCreate        bool
	ActiveCount      int32
	MaxActive        int32
	HasPendingRelease bool
}

func checkBuyerGate(db *gorm.DB, buyerAgentID int64, maxActive int) (*GateResult, error) {
	activeCount, err := dal.CountActiveOrders(db, buyerAgentID)
	if err != nil {
		return nil, err
	}

	hasPending, err := dal.HasPendingRelease(db, buyerAgentID)
	if err != nil {
		return nil, err
	}

	result := &GateResult{
		ActiveCount:       int32(activeCount),
		MaxActive:         int32(maxActive),
		HasPendingRelease: hasPending,
	}
	result.CanCreate = activeCount < int64(maxActive) && !hasPending
	return result, nil
}
```

- [ ] **Step 2: Commit**

```bash
git add rpc/trade/gate.go
git commit -m "feat(trade): add buyer gate logic"
```

---

## Task 8: Trade RPC Handler

**Files:**
- Create: `rpc/trade/handler.go`

- [ ] **Step 1: Write handler with all RPC methods**

```go
package main

import (
	"context"
	"fmt"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/trade"
	"eigenflux_server/pkg/chief"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/rpc/trade/dal"
)

type TradeServiceImpl struct {
	serviceIDGen idgen.IDGenerator
	orderIDGen   idgen.IDGenerator
	eventIDGen   idgen.IDGenerator
	receiptIDGen idgen.IDGenerator
	chiefClient  *chief.Client
	maxActive    int
}

func ok() *base.BaseResp     { return &base.BaseResp{Code: 0, Msg: "success"} }
func fail(msg string) *base.BaseResp { return &base.BaseResp{Code: 500, Msg: msg} }
func badReq(msg string) *base.BaseResp { return &base.BaseResp{Code: 400, Msg: msg} }

func int64Ptr(v int64) *int64 { return &v }

// --- Service Declaration ---

func (s *TradeServiceImpl) PublishService(_ context.Context, req *trade.PublishServiceReq) (*trade.PublishServiceResp, error) {
	id, err := s.serviceIDGen.NextID()
	if err != nil {
		return &trade.PublishServiceResp{BaseResp: fail("id gen: " + err.Error())}, nil
	}

	svc := &dal.TradingService{
		ServiceID:          id,
		SellerAgentID:      req.SellerAgentId,
		Title:              req.Title,
		CapabilityDesc:     req.CapabilityDesc,
		CallSpecText:       req.CallSpecText,
		CallSpecSchema:     &req.CallSpecSchema,
		PriceText:          req.PriceText,
		AmountAtomic:       req.AmountAtomic,
		Asset:              req.Asset,
		DeliveryDeadlineMs: req.DeliveryDeadlineMs,
	}
	if err := dal.CreateService(db.DB, svc); err != nil {
		return &trade.PublishServiceResp{BaseResp: fail("create service: " + err.Error())}, nil
	}

	_ = mq.Publish(context.Background(), "stream:trade:service", map[string]interface{}{
		"service_id": fmt.Sprintf("%d", id),
		"action":     "publish",
	})

	return &trade.PublishServiceResp{ServiceId: id, BaseResp: ok()}, nil
}

func (s *TradeServiceImpl) UpdateService(_ context.Context, req *trade.UpdateServiceReq) (*trade.UpdateServiceResp, error) {
	updates := map[string]interface{}{
		"title":               req.Title,
		"capability_desc":     req.CapabilityDesc,
		"call_spec_text":      req.CallSpecText,
		"call_spec_schema":    req.CallSpecSchema,
		"price_text":          req.PriceText,
		"amount_atomic":       req.AmountAtomic,
		"asset":               req.Asset,
		"delivery_deadline_ms": req.DeliveryDeadlineMs,
	}
	if err := dal.UpdateService(db.DB, req.ServiceId, req.SellerAgentId, updates); err != nil {
		return &trade.UpdateServiceResp{BaseResp: fail("update: " + err.Error())}, nil
	}

	_ = mq.Publish(context.Background(), "stream:trade:service", map[string]interface{}{
		"service_id": fmt.Sprintf("%d", req.ServiceId),
		"action":     "update",
	})

	return &trade.UpdateServiceResp{BaseResp: ok()}, nil
}

func (s *TradeServiceImpl) OfflineService(_ context.Context, req *trade.OfflineServiceReq) (*trade.OfflineServiceResp, error) {
	if err := dal.OfflineService(db.DB, req.ServiceId, req.SellerAgentId); err != nil {
		return &trade.OfflineServiceResp{BaseResp: fail("offline: " + err.Error())}, nil
	}
	return &trade.OfflineServiceResp{BaseResp: ok()}, nil
}

func (s *TradeServiceImpl) GetMyServices(_ context.Context, req *trade.GetMyServicesReq) (*trade.GetMyServicesResp, error) {
	limit := int(req.Limit)
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var cursor int64
	if req.Cursor != "" {
		fmt.Sscanf(req.Cursor, "%d", &cursor)
	}

	services, err := dal.ListServicesBySeller(db.DB, req.SellerAgentId, limit+1, cursor)
	if err != nil {
		return &trade.GetMyServicesResp{BaseResp: fail("list: " + err.Error())}, nil
	}

	var nextCursor string
	if len(services) > limit {
		nextCursor = fmt.Sprintf("%d", services[limit-1].ServiceID)
		services = services[:limit]
	}

	items := make([]*trade.TradingService, len(services))
	for i, svc := range services {
		items[i] = svcToThrift(svc)
	}

	return &trade.GetMyServicesResp{Services: items, NextCursor: nextCursor, BaseResp: ok()}, nil
}

// --- Orders ---

func (s *TradeServiceImpl) CreateOrder(ctx context.Context, req *trade.CreateOrderReq) (*trade.CreateOrderResp, error) {
	gate, err := checkBuyerGate(db.DB, req.BuyerAgentId, s.maxActive)
	if err != nil {
		return &trade.CreateOrderResp{BaseResp: fail("gate check: " + err.Error())}, nil
	}
	if !gate.CanCreate {
		return &trade.CreateOrderResp{BaseResp: badReq("buyer gate blocked")}, nil
	}

	svc, err := dal.GetService(db.DB, req.ServiceId)
	if err != nil {
		return &trade.CreateOrderResp{BaseResp: fail("service not found: " + err.Error())}, nil
	}
	if svc.Status != dal.ServiceStatusActive {
		return &trade.CreateOrderResp{BaseResp: badReq("service not active")}, nil
	}
	if svc.SellerAgentID == req.BuyerAgentId {
		return &trade.CreateOrderResp{BaseResp: badReq("cannot buy own service")}, nil
	}

	orderID, err := s.orderIDGen.NextID()
	if err != nil {
		return &trade.CreateOrderResp{BaseResp: fail("id gen: " + err.Error())}, nil
	}

	order := &dal.TradeOrder{
		OrderID:                  orderID,
		ServiceID:                req.ServiceId,
		BuyerAgentID:             req.BuyerAgentId,
		SellerAgentID:            svc.SellerAgentID,
		Status:                   dal.OrderStatusCreated,
		FrozenTitle:              svc.Title,
		FrozenCallSpecText:       svc.CallSpecText,
		FrozenCallSpecSchema:     svc.CallSpecSchema,
		FrozenAmountAtomic:       svc.AmountAtomic,
		FrozenAsset:              svc.Asset,
		FrozenDeliveryDeadlineMs: svc.DeliveryDeadlineMs,
		BuyerInput:               req.BuyerInput,
	}
	if err := dal.CreateOrder(db.DB, order); err != nil {
		return &trade.CreateOrderResp{BaseResp: fail("create order: " + err.Error())}, nil
	}

	s.appendEvent(orderID, "created", req.BuyerAgentId, "")
	return &trade.CreateOrderResp{OrderId: orderID, BaseResp: ok()}, nil
}

func (s *TradeServiceImpl) EscrowSync(_ context.Context, req *trade.EscrowSyncReq) (*trade.EscrowSyncResp, error) {
	order, err := dal.GetOrder(db.DB, req.OrderId)
	if err != nil {
		return &trade.EscrowSyncResp{BaseResp: fail("order not found")}, nil
	}

	if req.EscrowStatus == "locked" {
		if err := validateTransition(order.Status, dal.OrderStatusEscrowLocked); err != nil {
			return &trade.EscrowSyncResp{BaseResp: badReq(err.Error())}, nil
		}
		now := time.Now().UnixMilli()
		if err := dal.UpdateOrderStatus(db.DB, req.OrderId, map[string]interface{}{
			"status":           dal.OrderStatusEscrowLocked,
			"escrow_id":        req.EscrowId,
			"escrow_status":    "locked",
			"escrow_locked_at": now,
		}); err != nil {
			return &trade.EscrowSyncResp{BaseResp: fail("update: " + err.Error())}, nil
		}
		s.appendEvent(req.OrderId, "escrow_locked", req.ActorAgentId, "")
	}

	s.saveReceipt(req.OrderId, req.EscrowId, req.EscrowStatus, req.RawPayload)
	return &trade.EscrowSyncResp{BaseResp: ok()}, nil
}

func (s *TradeServiceImpl) DeliverOrder(_ context.Context, req *trade.DeliverOrderReq) (*trade.DeliverOrderResp, error) {
	order, err := dal.GetOrder(db.DB, req.OrderId)
	if err != nil {
		return &trade.DeliverOrderResp{BaseResp: fail("order not found")}, nil
	}
	if order.SellerAgentID != req.SellerAgentId {
		return &trade.DeliverOrderResp{BaseResp: badReq("not seller")}, nil
	}
	if err := validateTransition(order.Status, dal.OrderStatusDelivered); err != nil {
		return &trade.DeliverOrderResp{BaseResp: badReq(err.Error())}, nil
	}

	now := time.Now().UnixMilli()
	if err := dal.UpdateOrderStatus(db.DB, req.OrderId, map[string]interface{}{
		"status":           dal.OrderStatusDelivered,
		"delivery_payload": req.DeliveryPayload,
		"delivered_at":     now,
	}); err != nil {
		return &trade.DeliverOrderResp{BaseResp: fail("update: " + err.Error())}, nil
	}

	s.appendEvent(req.OrderId, "delivered", req.SellerAgentId, "")
	return &trade.DeliverOrderResp{BaseResp: ok()}, nil
}

func (s *TradeServiceImpl) ReleaseOrder(ctx context.Context, req *trade.ReleaseOrderReq) (*trade.ReleaseOrderResp, error) {
	order, err := dal.GetOrder(db.DB, req.OrderId)
	if err != nil {
		return &trade.ReleaseOrderResp{BaseResp: fail("order not found")}, nil
	}
	if order.BuyerAgentID != req.BuyerAgentId {
		return &trade.ReleaseOrderResp{BaseResp: badReq("not buyer")}, nil
	}
	if err := validateTransition(order.Status, dal.OrderStatusReleased); err != nil {
		return &trade.ReleaseOrderResp{BaseResp: badReq(err.Error())}, nil
	}

	if err := s.chiefClient.ReleaseEscrow(ctx, order.EscrowID); err != nil {
		return &trade.ReleaseOrderResp{BaseResp: fail("chief release: " + err.Error())}, nil
	}

	now := time.Now().UnixMilli()
	if err := dal.UpdateOrderStatus(db.DB, req.OrderId, map[string]interface{}{
		"status":        dal.OrderStatusReleased,
		"escrow_status": "released",
		"released_at":   now,
		"closed_at":     now,
	}); err != nil {
		return &trade.ReleaseOrderResp{BaseResp: fail("update: " + err.Error())}, nil
	}

	s.appendEvent(req.OrderId, "released", req.BuyerAgentId, "")
	s.saveReceipt(req.OrderId, order.EscrowID, "released", "")
	s.publishOrderEvent(req.OrderId, order.ServiceID, "released")
	return &trade.ReleaseOrderResp{BaseResp: ok()}, nil
}

func (s *TradeServiceImpl) RefundOrder(ctx context.Context, req *trade.RefundOrderReq) (*trade.RefundOrderResp, error) {
	order, err := dal.GetOrder(db.DB, req.OrderId)
	if err != nil {
		return &trade.RefundOrderResp{BaseResp: fail("order not found")}, nil
	}
	if err := validateTransition(order.Status, dal.OrderStatusRefunded); err != nil {
		return &trade.RefundOrderResp{BaseResp: badReq(err.Error())}, nil
	}

	if order.EscrowID != "" {
		if err := s.chiefClient.RefundEscrow(ctx, order.EscrowID); err != nil {
			return &trade.RefundOrderResp{BaseResp: fail("chief refund: " + err.Error())}, nil
		}
	}

	now := time.Now().UnixMilli()
	if err := dal.UpdateOrderStatus(db.DB, req.OrderId, map[string]interface{}{
		"status":        dal.OrderStatusRefunded,
		"escrow_status": "refunded",
		"refunded_at":   now,
		"closed_at":     now,
	}); err != nil {
		return &trade.RefundOrderResp{BaseResp: fail("update: " + err.Error())}, nil
	}

	s.appendEvent(req.OrderId, "refunded", req.ActorAgentId, "")
	s.saveReceipt(req.OrderId, order.EscrowID, "refunded", "")
	s.publishOrderEvent(req.OrderId, order.ServiceID, "refunded")
	return &trade.RefundOrderResp{BaseResp: ok()}, nil
}

func (s *TradeServiceImpl) GetOrder(_ context.Context, req *trade.GetOrderReq) (*trade.GetOrderResp, error) {
	order, err := dal.GetOrder(db.DB, req.OrderId)
	if err != nil {
		return &trade.GetOrderResp{BaseResp: fail("order not found")}, nil
	}
	if order.BuyerAgentID != req.AgentId && order.SellerAgentID != req.AgentId {
		return &trade.GetOrderResp{BaseResp: badReq("not authorized")}, nil
	}

	events, _ := dal.ListOrderEvents(db.DB, req.OrderId)
	return &trade.GetOrderResp{
		Order:    orderToThrift(order),
		Events:   eventsToThrift(events),
		BaseResp: ok(),
	}, nil
}

func (s *TradeServiceImpl) ListOrders(_ context.Context, req *trade.ListOrdersReq) (*trade.ListOrdersResp, error) {
	limit := int(req.Limit)
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var cursor int64
	if req.Cursor != "" {
		fmt.Sscanf(req.Cursor, "%d", &cursor)
	}

	orders, err := dal.ListOrdersByAgent(db.DB, req.AgentId, req.Role, req.StatusFilter, limit+1, cursor)
	if err != nil {
		return &trade.ListOrdersResp{BaseResp: fail("list: " + err.Error())}, nil
	}

	var nextCursor string
	if len(orders) > limit {
		nextCursor = fmt.Sprintf("%d", orders[limit-1].OrderID)
		orders = orders[:limit]
	}

	items := make([]*trade.TradeOrder, len(orders))
	for i, o := range orders {
		items[i] = orderToThrift(o)
	}

	return &trade.ListOrdersResp{Orders: items, NextCursor: nextCursor, BaseResp: ok()}, nil
}

func (s *TradeServiceImpl) GetGateStatus(_ context.Context, req *trade.GetGateStatusReq) (*trade.GetGateStatusResp, error) {
	gate, err := checkBuyerGate(db.DB, req.BuyerAgentId, s.maxActive)
	if err != nil {
		return &trade.GetGateStatusResp{BaseResp: fail("gate: " + err.Error())}, nil
	}
	return &trade.GetGateStatusResp{
		CanCreateOrder:    gate.CanCreate,
		ActiveOrderCount:  gate.ActiveCount,
		MaxActiveOrders:   gate.MaxActive,
		HasPendingRelease: gate.HasPendingRelease,
		BaseResp:          ok(),
	}, nil
}

func (s *TradeServiceImpl) SearchServices(_ context.Context, _ *trade.SearchServicesReq) (*trade.SearchServicesResp, error) {
	// Placeholder — actual search is done in Sort RPC or via ES directly in a later task.
	return &trade.SearchServicesResp{BaseResp: badReq("use Sort RPC SearchServices")}, nil
}

// --- Helpers ---

func (s *TradeServiceImpl) appendEvent(orderID int64, eventType string, actorID int64, payload string) {
	eventID, err := s.eventIDGen.NextID()
	if err != nil {
		return
	}
	event := &dal.TradeOrderEvent{
		EventID:      eventID,
		OrderID:      orderID,
		EventType:    eventType,
		ActorAgentID: actorID,
	}
	if payload != "" {
		event.PayloadJSON = &payload
	}
	_ = dal.CreateOrderEvent(db.DB, event)
}

func (s *TradeServiceImpl) saveReceipt(orderID int64, escrowID, status, rawPayload string) {
	receiptID, err := s.receiptIDGen.NextID()
	if err != nil {
		return
	}
	receipt := &dal.TradeEscrowReceipt{
		ReceiptID:    receiptID,
		OrderID:      orderID,
		EscrowID:     escrowID,
		EscrowStatus: status,
	}
	if rawPayload != "" {
		receipt.RawPayload = &rawPayload
	}
	_ = dal.CreateEscrowReceipt(db.DB, receipt)
}

func (s *TradeServiceImpl) publishOrderEvent(orderID, serviceID int64, eventType string) {
	_ = mq.Publish(context.Background(), "stream:trade:order-event", map[string]interface{}{
		"order_id":   fmt.Sprintf("%d", orderID),
		"service_id": fmt.Sprintf("%d", serviceID),
		"event_type": eventType,
	})
}

// --- Thrift converters ---

func svcToThrift(s *dal.TradingService) *trade.TradingService {
	ts := &trade.TradingService{
		ServiceId:          s.ServiceID,
		SellerAgentId:      s.SellerAgentID,
		Title:              s.Title,
		CapabilityDesc:     s.CapabilityDesc,
		CallSpecText:       s.CallSpecText,
		PriceText:          s.PriceText,
		AmountAtomic:       s.AmountAtomic,
		Asset:              s.Asset,
		DeliveryDeadlineMs: s.DeliveryDeadlineMs,
		Status:             s.Status,
		SuccessRate:        s.SuccessRate,
		AvgLatencyMs:       s.AvgLatencyMs,
		OrderCount:         s.OrderCount,
		ReleasedCount:      s.ReleasedCount,
		RefundedCount:      s.RefundedCount,
		ExpiredCount:       s.ExpiredCount,
		CreatedAt:          s.CreatedAt,
		UpdatedAt:          s.UpdatedAt,
	}
	if s.CallSpecSchema != nil {
		ts.CallSpecSchema = *s.CallSpecSchema
	}
	return ts
}

func orderToThrift(o *dal.TradeOrder) *trade.TradeOrder {
	to := &trade.TradeOrder{
		OrderId:                  o.OrderID,
		ServiceId:                o.ServiceID,
		BuyerAgentId:             o.BuyerAgentID,
		SellerAgentId:            o.SellerAgentID,
		Status:                   o.Status,
		EscrowId:                 o.EscrowID,
		EscrowStatus:             o.EscrowStatus,
		FrozenTitle:              o.FrozenTitle,
		FrozenCallSpecText:       o.FrozenCallSpecText,
		FrozenAmountAtomic:       o.FrozenAmountAtomic,
		FrozenAsset:              o.FrozenAsset,
		FrozenDeliveryDeadlineMs: o.FrozenDeliveryDeadlineMs,
		BuyerInput:               o.BuyerInput,
		DeliveryPayload:          o.DeliveryPayload,
		CreatedAt:                o.CreatedAt,
		DeadlineAt:               o.DeadlineAt,
	}
	if o.FrozenCallSpecSchema != nil {
		to.FrozenCallSpecSchema = *o.FrozenCallSpecSchema
	}
	if o.EscrowLockedAt != nil {
		to.EscrowLockedAt = *o.EscrowLockedAt
	}
	if o.DeliveredAt != nil {
		to.DeliveredAt = *o.DeliveredAt
	}
	if o.ReleasedAt != nil {
		to.ReleasedAt = *o.ReleasedAt
	}
	if o.RefundedAt != nil {
		to.RefundedAt = *o.RefundedAt
	}
	if o.ClosedAt != nil {
		to.ClosedAt = *o.ClosedAt
	}
	return to
}

func eventsToThrift(events []*dal.TradeOrderEvent) []*trade.TradeOrderEvent {
	result := make([]*trade.TradeOrderEvent, len(events))
	for i, e := range events {
		te := &trade.TradeOrderEvent{
			EventId:      e.EventID,
			OrderId:      e.OrderID,
			EventType:    e.EventType,
			ActorAgentId: e.ActorAgentID,
			CreatedAt:    e.CreatedAt,
		}
		if e.PayloadJSON != nil {
			te.PayloadJson = *e.PayloadJSON
		}
		result[i] = te
	}
	return result
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./rpc/trade/`
Expected: may fail until main.go exists; verify with `go vet ./rpc/trade/handler.go`

- [ ] **Step 3: Commit**

```bash
git add rpc/trade/handler.go
git commit -m "feat(trade): add Trade RPC handler with all methods"
```

---

## Task 9: Trade RPC Main

**Files:**
- Create: `rpc/trade/main.go`

- [ ] **Step 1: Write main.go**

```go
package main

import (
	"context"
	"log"
	"net"
	"strings"

	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/trade/tradeservice"
	"eigenflux_server/pkg/chief"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/pkg/telemetry"
)

func main() {
	cfg := config.Load()
	logFlush := logger.Init("TradeService", cfg.EffectiveLokiURL(), cfg.LogLevel)
	defer logFlush()

	shutdown, err := telemetry.Init("TradeService", cfg.OtelExporterEndpoint, cfg.MonitorEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	go metrics.StartMetricsServer(cfg.TradeRPCPort + 1000)

	db.Init(cfg.PgDSN)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)

	etcdEndpoints := splitEtcdEndpoints(cfg.EtcdAddr)

	serviceIDGen := mustIDGen(etcdEndpoints, cfg, "trade-service-id")
	defer func() { _ = serviceIDGen.Close(context.Background()) }()

	orderIDGen := mustIDGen(etcdEndpoints, cfg, "trade-order-id")
	defer func() { _ = orderIDGen.Close(context.Background()) }()

	eventIDGen := mustIDGen(etcdEndpoints, cfg, "trade-event-id")
	defer func() { _ = eventIDGen.Close(context.Background()) }()

	receiptIDGen := mustIDGen(etcdEndpoints, cfg, "trade-receipt-id")
	defer func() { _ = receiptIDGen.Close(context.Background()) }()

	chiefClient := chief.NewClient(cfg.ChiefLedgerURL)

	r, err := etcd.NewEtcdRegistry(etcdEndpoints)
	if err != nil {
		log.Fatalf("failed to create etcd registry: %v", err)
	}

	listenAddr := cfg.ListenAddr(cfg.TradeRPCPort)
	addr, _ := net.ResolveTCPAddr("tcp", listenAddr)
	svr := tradeservice.NewServer(
		&TradeServiceImpl{
			serviceIDGen: serviceIDGen,
			orderIDGen:   orderIDGen,
			eventIDGen:   eventIDGen,
			receiptIDGen: receiptIDGen,
			chiefClient:  chiefClient,
			maxActive:    cfg.TradeMaxActiveOrders,
		},
		rpcx.ServerOptions(addr, r, "TradeService", metrics.KitexServerMW())...,
	)

	log.Printf("Trade RPC starting on %s", listenAddr)
	if err := svr.Run(); err != nil {
		log.Fatalf("trade service failed: %v", err)
	}
}

func mustIDGen(endpoints []string, cfg *config.Config, name string) *idgen.ManagedGenerator {
	gen, err := idgen.NewManagedGenerator(context.Background(), idgen.ManagedGeneratorConfig{
		Endpoints:      endpoints,
		WorkerPrefix:   cfg.IDWorkerPrefix,
		ServiceName:    name,
		InstanceID:     cfg.IDInstanceID,
		LeaseTTLSecond: cfg.IDWorkerLeaseTTL,
		EpochMS:        cfg.IDSnowflakeEpoch,
	})
	if err != nil {
		log.Fatalf("failed to init %s generator: %v", name, err)
	}
	log.Printf("%s generator ready: worker_id=%d", name, gen.WorkerID())
	return gen
}

func splitEtcdEndpoints(raw string) []string {
	parts := strings.Split(raw, ",")
	endpoints := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			endpoints = append(endpoints, p)
		}
	}
	if len(endpoints) == 0 {
		return []string{"localhost:2379"}
	}
	return endpoints
}
```

- [ ] **Step 2: Verify full build**

Run: `go build -o build/trade ./rpc/trade/`
Expected: binary created at `build/trade`

- [ ] **Step 3: Commit**

```bash
git add rpc/trade/main.go
git commit -m "feat(trade): add Trade RPC service entry point"
```

---

## Task 10: ES Services Index Setup

**Files:**
- Create: `pkg/es/services_mapping.go`
- Create: `pkg/es/services_ilm.go`
- Modify: `pkg/es/client.go`

- [ ] **Step 1: Write services_mapping.go**

```go
package es

func BuildServicesMapping(embeddingDims int) map[string]interface{} {
	return map[string]interface{}{
		"properties": map[string]interface{}{
			"service_id":          map[string]interface{}{"type": "long"},
			"seller_agent_id":     map[string]interface{}{"type": "long"},
			"title":               map[string]interface{}{"type": "text", "analyzer": "standard"},
			"capability_desc":     map[string]interface{}{"type": "text", "analyzer": "standard"},
			"call_spec_text":      map[string]interface{}{"type": "text", "analyzer": "standard"},
			"keywords":            map[string]interface{}{"type": "keyword", "normalizer": "lowercase_normalizer"},
			"domains":             map[string]interface{}{"type": "keyword", "normalizer": "lowercase_normalizer"},
			"embedding":           map[string]interface{}{"type": "dense_vector", "dims": embeddingDims, "index": true, "similarity": "cosine"},
			"amount_atomic":       map[string]interface{}{"type": "long"},
			"asset":               map[string]interface{}{"type": "keyword"},
			"delivery_deadline_ms": map[string]interface{}{"type": "long"},
			"success_rate":        map[string]interface{}{"type": "float"},
			"avg_latency_ms":      map[string]interface{}{"type": "long"},
			"order_count":         map[string]interface{}{"type": "integer"},
			"updated_at":          map[string]interface{}{"type": "date", "format": "strict_date_optional_time||epoch_millis"},
		},
	}
}
```

- [ ] **Step 2: Write services_ilm.go**

```go
package es

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/json"
	"eigenflux_server/pkg/logger"
	"fmt"
)

const (
	ServicesIndexName     = "services"
	ServicesReadPattern   = "services-*"
	ServicesILMPolicy     = "services-policy"
	ServicesTemplateName  = "services-template"
	ServicesInitialIndex  = "services-000001"
)

func buildServicesTemplate(embeddingDims int) map[string]interface{} {
	shards := getEnvInt("ES_SHARDS", 1)
	replicas := getEnvInt("ES_REPLICAS", 0)

	return map[string]interface{}{
		"index_patterns": []string{"services-*"},
		"template": map[string]interface{}{
			"settings": map[string]interface{}{
				"number_of_shards":               shards,
				"number_of_replicas":             replicas,
				"refresh_interval":               "30s",
				"index.lifecycle.name":           ServicesILMPolicy,
				"index.lifecycle.rollover_alias": ServicesIndexName,
				"analysis": map[string]interface{}{
					"normalizer": map[string]interface{}{
						"lowercase_normalizer": map[string]interface{}{
							"type":   "custom",
							"filter": []string{"lowercase"},
						},
					},
				},
			},
			"mappings": BuildServicesMapping(embeddingDims),
		},
	}
}

func SetupServicesILM(ctx context.Context, embeddingDims int) error {
	if err := upsertServicesILMPolicy(ctx); err != nil {
		return fmt.Errorf("upsert services ILM policy: %w", err)
	}
	if err := upsertServicesTemplate(ctx, embeddingDims); err != nil {
		return fmt.Errorf("upsert services template: %w", err)
	}
	if err := bootstrapServicesIfNeeded(ctx); err != nil {
		return fmt.Errorf("bootstrap services index: %w", err)
	}
	return nil
}

func upsertServicesILMPolicy(ctx context.Context) error {
	body, err := json.Marshal(ilmPolicy) // reuse same lifecycle as items
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	res, err := Client.ILM.PutLifecycle(ServicesILMPolicy,
		Client.ILM.PutLifecycle.WithContext(ctx),
		Client.ILM.PutLifecycle.WithBody(bytes.NewReader(body)))
	if err != nil {
		return fmt.Errorf("put ILM: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return fmt.Errorf("ILM error: %s", res.String())
	}
	logger.Default().Info("services ILM policy upserted", "policy", ServicesILMPolicy)
	return nil
}

func upsertServicesTemplate(ctx context.Context, embeddingDims int) error {
	template := buildServicesTemplate(embeddingDims)
	body, err := json.Marshal(template)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	res, err := Client.Indices.PutIndexTemplate(ServicesTemplateName,
		bytes.NewReader(body),
		Client.Indices.PutIndexTemplate.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("put template: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return fmt.Errorf("template error: %s", res.String())
	}
	logger.Default().Info("services index template upserted", "template", ServicesTemplateName)
	return nil
}

func bootstrapServicesIfNeeded(ctx context.Context) error {
	aliasRes, err := Client.Indices.GetAlias(
		Client.Indices.GetAlias.WithContext(ctx),
		Client.Indices.GetAlias.WithName(ServicesIndexName))
	if err != nil {
		return fmt.Errorf("check alias: %w", err)
	}
	defer aliasRes.Body.Close()
	if aliasRes.StatusCode == 200 {
		logger.Default().Info("services write alias exists", "alias", ServicesIndexName)
		return nil
	}

	initialMapping := map[string]interface{}{
		"aliases": map[string]interface{}{
			ServicesIndexName: map[string]interface{}{"is_write_index": true},
		},
	}
	body, _ := json.Marshal(initialMapping)
	createRes, err := Client.Indices.Create(ServicesInitialIndex,
		Client.Indices.Create.WithContext(ctx),
		Client.Indices.Create.WithBody(bytes.NewReader(body)))
	if err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	defer createRes.Body.Close()
	if createRes.IsError() && !isAlreadyExistsCreateError(createRes.StatusCode, nil) {
		return fmt.Errorf("create error: %s", createRes.String())
	}
	logger.Default().Info("services ILM bootstrap done", "index", ServicesInitialIndex)
	return nil
}
```

- [ ] **Step 3: Add SetupServicesILM call to InitES**

In `pkg/es/client.go`, add after the existing `SetupILM` call (around line 63):

```go
if err := SetupServicesILM(context.Background(), expectedEmbeddingDims); err != nil {
	return fmt.Errorf("failed to setup services ILM: %w", err)
}
```

- [ ] **Step 4: Verify build**

Run: `go build ./pkg/es/`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add pkg/es/services_mapping.go pkg/es/services_ilm.go pkg/es/client.go
git commit -m "feat(trade): add services-* ES index with ILM"
```

---

## Task 11: Pipeline Consumers

**Files:**
- Create: `pipeline/consumer/service_consumer.go`
- Create: `pipeline/consumer/order_event_consumer.go`
- Modify: `pipeline/main.go`

- [ ] **Step 1: Write service_consumer.go**

This consumer listens to `stream:trade:service` and generates embeddings + indexes to ES. Follow the existing `ItemConsumer` pattern (worker pool, consumer group, ack).

The consumer reads the service from DB, concatenates `title + capability_desc + call_spec_text`, calls the embedding client, and indexes the document to ES `services` write alias.

- [ ] **Step 2: Write order_event_consumer.go**

This consumer listens to `stream:trade:order-event` and updates `trading_services` stats. On terminal events (released, refunded, expired), it increments the corresponding counter column and recalculates `success_rate`.

- [ ] **Step 3: Wire consumers in pipeline/main.go**

Add imports and start both consumers alongside existing ones. Add stream lag monitoring entries for `stream:trade:service` and `stream:trade:order-event`.

- [ ] **Step 4: Verify build**

Run: `go build ./pipeline/`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add pipeline/consumer/service_consumer.go pipeline/consumer/order_event_consumer.go pipeline/main.go
git commit -m "feat(trade): add service and order-event pipeline consumers"
```

---

## Task 12: Trade Expiry Cron

**Files:**
- Create: `pipeline/cron/trade_expiry.go`
- Modify: `pipeline/cron/main.go`

- [ ] **Step 1: Write trade_expiry.go**

```go
package main

import (
	"context"
	"time"

	"eigenflux_server/pkg/chief"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	tradedal "eigenflux_server/rpc/trade/dal"

	"github.com/redis/go-redis/v9"
)

func StartTradeExpiryScanner(ctx context.Context, cfg *config.Config, rdb *redis.Client) {
	interval := time.Duration(cfg.TradeExpiryScanIntervalSec) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	chiefClient := chief.NewClient(cfg.ChiefLedgerURL)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scanExpiredOrders(ctx, chiefClient)
		}
	}
}

func scanExpiredOrders(ctx context.Context, chiefClient *chief.Client) {
	nowMs := time.Now().UnixMilli()
	orders, err := tradedal.FindExpiredOrders(db.DB, nowMs, 100)
	if err != nil {
		logger.Default().Warn("trade expiry scan failed", "err", err)
		return
	}

	for _, order := range orders {
		if err := tradedal.UpdateOrderStatus(db.DB, order.OrderID, map[string]interface{}{
			"status":    tradedal.OrderStatusExpired,
			"closed_at": nowMs,
		}); err != nil {
			logger.Default().Warn("trade expiry update failed", "order_id", order.OrderID, "err", err)
			continue
		}

		if order.EscrowID != "" {
			if err := chiefClient.RefundEscrow(ctx, order.EscrowID); err != nil {
				logger.Default().Warn("trade expiry refund failed", "order_id", order.OrderID, "err", err)
			}
		}

		logger.Default().Info("trade order expired", "order_id", order.OrderID)
	}
}
```

- [ ] **Step 2: Add to cron/main.go**

Add after the existing cron jobs in `main()`:

```go
go StartTradeExpiryScanner(ctx, cfg, mq.RDB)
```

Add import for chief and trade dal packages.

- [ ] **Step 3: Verify build**

Run: `go build ./pipeline/cron/`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add pipeline/cron/trade_expiry.go pipeline/cron/main.go
git commit -m "feat(trade): add order expiry cron scanner"
```

---

## Task 13: API Gateway Integration

**Files:**
- Modify: `api/clients/clients.go`
- Modify: `api/main.go`

- [ ] **Step 1: Add TradeClient to clients.go**

Add import:
```go
"eigenflux_server/kitex_gen/eigenflux/trade/tradeservice"
```

Add to var block:
```go
TradeClient tradeservice.Client
```

- [ ] **Step 2: Add Trade RPC client initialization to api/main.go**

Add import:
```go
"eigenflux_server/kitex_gen/eigenflux/trade/tradeservice"
```

Add after `notificationClient` initialization (around line 107):

```go
tradeClient, err := tradeservice.NewClient("TradeService", rpcx.ClientOptions(r)...)
if err != nil {
	log.Fatalf("failed to create trade client: %v", err)
}
log.Println("Trade RPC client initialized")
```

Add after `clients.NotificationClient = notificationClient`:
```go
clients.TradeClient = tradeClient
```

- [ ] **Step 3: Verify build**

Run: `go build ./api/`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add api/clients/clients.go api/main.go
git commit -m "feat(trade): wire Trade RPC client in API gateway"
```

Note: HTTP endpoint handlers (hz-generated) will be added in a separate task after running `hz` code generation. For V1, the RPC client is wired and ready.

---

## Task 14: Build & Start Scripts

**Files:**
- Modify: `scripts/common/build.sh`
- Modify: `scripts/local/start_local.sh`

- [ ] **Step 1: Add trade to build.sh ALL_SERVICES**

Add after `"notification:./rpc/notification/"`:
```bash
"trade:./rpc/trade/"
```

- [ ] **Step 2: Add trade to start_local.sh**

Add `TRADE_RPC_PORT` variable:
```bash
TRADE_RPC_PORT="${TRADE_RPC_PORT:-8888}"
```

Add to `SERVICE_MAP` after `"notification:${NOTIFICATION_RPC_PORT}"`:
```bash
"trade:${TRADE_RPC_PORT}"
```

- [ ] **Step 3: Verify build**

Run: `bash scripts/common/build.sh trade`
Expected: `Compiling trade ... OK`

- [ ] **Step 4: Commit**

```bash
git add scripts/common/build.sh scripts/local/start_local.sh
git commit -m "feat(trade): add trade service to build and startup scripts"
```

---

## Task 15: E2E Tests

**Files:**
- Create: `tests/trade/trade_test.go`

- [ ] **Step 1: Write trade e2e tests**

Tests should cover:
1. Publish a trading service
2. Get my services
3. Update a service
4. Offline a service
5. Create an order (with buyer gate passing)
6. Escrow sync (lock)
7. Deliver an order
8. Release an order
9. Buyer gate blocking (max active, pending release)
10. Order expiry (create with short deadline, wait, verify expired)

Follow the existing pattern from `tests/sort/sort_test.go`:
- `TestMain` with `testutil.RunTestMain`
- Advisory lock for test isolation
- Config load, DB init, Redis init
- Direct RPC calls to Trade service

- [ ] **Step 2: Verify tests run**

Run: `go test -v ./tests/trade/ -run TestPublishService`
Expected: PASS (requires all services running)

- [ ] **Step 3: Commit**

```bash
git add tests/trade/
git commit -m "feat(trade): add trading system e2e tests"
```

---

## Task 16: Documentation Updates

**Files:**
- Modify: `CLAUDE.md`
- Create: `docs/dev/trading.md`

- [ ] **Step 1: Update CLAUDE.md directory table**

Add row to Directory Responsibilities table:
```
| `rpc/trade/` | Trade RPC service | Kitex-based (port 8888). Service declarations, order management, escrow sync, buyer gate. Business logic in `handler.go`, data access in `dal/` |
```

Add to Module Documentation table:
```
| `trading.md` | Trading service declarations, order lifecycle, Chief escrow integration, buyer gate, ES service discovery |
```

- [ ] **Step 2: Write docs/dev/trading.md**

Document: service architecture, order state machine, buyer gate rules, Chief integration, ES services index, API endpoints, configuration variables.

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md docs/dev/trading.md
git commit -m "docs: add trading system documentation"
```
