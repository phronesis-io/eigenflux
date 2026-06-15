-- +goose Up
-- +goose StatementBegin
--
-- trading_services holds the slow-changing meta of a service declaration only.
-- Order-driven counters (success_rate, avg_latency_ms, *_count) and the
-- "last activity" timestamp live in trading_service_stats so the meta row is
-- not rewritten on every order event. See docs/dev/trading.md for the rank-time
-- join pattern. The LLM-enriched columns (capability_tags / use_cases /
-- canonical_inputs / canonical_outputs / enrichment_version) are written by
-- pipeline/consumer/service_enrich.go — see
-- docs/superpowers/specs/2026-06-09-task-to-service-search-design.md.
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
    created_at           BIGINT NOT NULL,
    updated_at           BIGINT NOT NULL,
    indexed_at           BIGINT NOT NULL DEFAULT 0,
    capability_tags      TEXT[]   NOT NULL DEFAULT '{}'::TEXT[],
    use_cases            TEXT     NOT NULL DEFAULT '',
    canonical_inputs     JSONB    NOT NULL DEFAULT '[]'::JSONB,
    canonical_outputs    JSONB    NOT NULL DEFAULT '[]'::JSONB,
    enrichment_version   INT      NOT NULL DEFAULT 0,

    CONSTRAINT chk_trading_services_status CHECK (status IN (0, 1, 2)),
    CONSTRAINT chk_trading_services_amount CHECK (amount_atomic > 0),
    CONSTRAINT chk_trading_services_deadline CHECK (delivery_deadline_ms > 0)
);
CREATE INDEX idx_trading_services_seller ON trading_services(seller_agent_id, status);

--
-- trading_service_stats is the hot, frequently-updated counterpart to
-- trading_services. One row per service. order_event consumers UPSERT here;
-- ranker reads from here at search time via BatchGetServiceRankStats.
-- last_activity_at exposes a "recency" signal: the millisecond timestamp of
-- the most recent terminal order event (released/refunded/expired). The
-- ranker can convert this to a duration-from-now feature.
CREATE TABLE trading_service_stats (
    service_id        BIGINT PRIMARY KEY,
    order_count       INT NOT NULL DEFAULT 0,
    released_count    INT NOT NULL DEFAULT 0,
    refunded_count    INT NOT NULL DEFAULT 0,
    expired_count     INT NOT NULL DEFAULT 0,
    success_rate      DOUBLE PRECISION NOT NULL DEFAULT 0,
    avg_latency_ms    BIGINT NOT NULL DEFAULT 0,
    last_activity_at  BIGINT NOT NULL DEFAULT 0,
    updated_at        BIGINT NOT NULL
);
CREATE INDEX idx_trading_service_stats_recent ON trading_service_stats(last_activity_at DESC);

--
-- trading_service_stats_daily holds a full snapshot of every active service's
-- rolling stats once per day. The table is RANGE-partitioned by activity_date
-- so daily writes land in their own partition and old days can be detached
-- without rewriting active data. An initial partition for 2026-01-01 → epoch
-- is created so the parent is queryable from day one; a separate daily
-- snapshot cron (TBD) will pre-create future partitions and emit rows.
CREATE TABLE trading_service_stats_daily (
    activity_date    DATE NOT NULL,
    service_id       BIGINT NOT NULL,
    order_count      INT NOT NULL DEFAULT 0,
    released_count   INT NOT NULL DEFAULT 0,
    refunded_count   INT NOT NULL DEFAULT 0,
    expired_count    INT NOT NULL DEFAULT 0,
    success_rate     DOUBLE PRECISION NOT NULL DEFAULT 0,
    avg_latency_ms   BIGINT NOT NULL DEFAULT 0,
    last_activity_at BIGINT NOT NULL DEFAULT 0,
    snapshot_at      BIGINT NOT NULL,
    PRIMARY KEY (activity_date, service_id)
) PARTITION BY RANGE (activity_date);

CREATE TABLE trading_service_stats_daily_default
    PARTITION OF trading_service_stats_daily DEFAULT;

--
-- trade_orders tracks every buyer order against an active service. Payment is
-- a direct agent-to-agent transfer settled by the buyer's local kovaloop CLI;
-- the server verifies the resulting transfer_id at release time via
-- pkg/chief.VerifyAgentTransfer. Status codes are defined by the OrderStatus*
-- constants in rpc/trade/dal/db.go:
--   0=created, 2=delivered, 3=released, 5=expired, 6=refunded.
-- Slots 1 (escrow_locked) and 4 (seller_cancelled) are reserved for historical
-- compatibility but no current code path enters them.
CREATE TABLE trade_orders (
    order_id                    BIGINT PRIMARY KEY,
    service_id                  BIGINT NOT NULL,
    buyer_agent_id              BIGINT NOT NULL,
    seller_agent_id             BIGINT NOT NULL,
    status                      SMALLINT NOT NULL DEFAULT 0,
    transfer_id                 VARCHAR(200) NOT NULL DEFAULT '',
    transfer_state              VARCHAR(20) NOT NULL DEFAULT '',
    frozen_title                VARCHAR(200) NOT NULL,
    frozen_call_spec_text       TEXT NOT NULL DEFAULT '',
    frozen_call_spec_schema     JSONB,
    frozen_amount_atomic        BIGINT NOT NULL,
    frozen_asset                VARCHAR(20) NOT NULL,
    frozen_delivery_deadline_ms BIGINT NOT NULL,
    buyer_input                 TEXT NOT NULL DEFAULT '',
    idempotency_key             VARCHAR(64) NOT NULL DEFAULT '',
    delivery_payload            TEXT NOT NULL DEFAULT '',
    created_at                  BIGINT NOT NULL,
    deadline_at                 BIGINT NOT NULL,
    paid_at                     BIGINT,
    delivered_at                BIGINT,
    released_at                 BIGINT,
    refunded_at                 BIGINT,
    closed_at                   BIGINT,

    CONSTRAINT chk_trade_orders_status CHECK (status IN (0,1,2,3,4,5,6)),
    CONSTRAINT chk_trade_orders_amount CHECK (frozen_amount_atomic > 0)
);
CREATE INDEX idx_trade_orders_buyer ON trade_orders(buyer_agent_id, status);
CREATE INDEX idx_trade_orders_seller ON trade_orders(seller_agent_id, status);
CREATE INDEX idx_trade_orders_deadline ON trade_orders(deadline_at) WHERE status IN (0, 2);
CREATE UNIQUE INDEX idx_trade_orders_idemp
    ON trade_orders(buyer_agent_id, idempotency_key)
    WHERE idempotency_key <> '';

-- event_type values are an enum defined in rpc/trade/dal/db.go (EventType*
-- constants). 0=unknown, 1=created, 2=escrow_locked, 3=delivered, 4=released,
-- 5=refunded, 6=expired, 7=seller_cancelled. The 2 and 7 slots are kept in
-- the eventTypeNames map so historical rows still render, but no current code
-- emits them. Stored as SMALLINT rather than VARCHAR so the column stays
-- compact and DB-level checks can reject unknown codes.
CREATE TABLE trade_order_events (
    event_id       BIGINT PRIMARY KEY,
    order_id       BIGINT NOT NULL,
    event_type     SMALLINT NOT NULL,
    actor_agent_id BIGINT NOT NULL DEFAULT 0,
    payload_json   JSONB,
    created_at     BIGINT NOT NULL,

    CONSTRAINT chk_trade_order_events_type CHECK (event_type BETWEEN 1 AND 7)
);
CREATE INDEX idx_trade_order_events_order ON trade_order_events(order_id, created_at);

-- trade_transfer_receipts is the immutable log of every verified kovaloop
-- transfer recorded against an order. tx_hash / settlement_record_id / asset /
-- amount_atomic carry the fields extracted from the verified ledger entry.
CREATE TABLE trade_transfer_receipts (
    receipt_id           BIGINT PRIMARY KEY,
    order_id             BIGINT NOT NULL,
    transfer_id          VARCHAR(200) NOT NULL DEFAULT '',
    provider             VARCHAR(30) NOT NULL DEFAULT 'chief',
    transfer_state       VARCHAR(20) NOT NULL,
    provider_event_id    VARCHAR(200) NOT NULL DEFAULT '',
    tx_hash              VARCHAR(120) NOT NULL DEFAULT '',
    settlement_record_id VARCHAR(120) NOT NULL DEFAULT '',
    asset                VARCHAR(20)  NOT NULL DEFAULT '',
    amount_atomic        BIGINT       NOT NULL DEFAULT 0,
    raw_payload          JSONB,
    created_at           BIGINT NOT NULL
);
CREATE INDEX idx_trade_transfer_receipts_order ON trade_transfer_receipts(order_id);

-- trade_outbox is the transactional outbox for events that must reach
-- Redis Streams. Handlers insert rows in the same DB transaction that mutates
-- trade_orders; a polling dispatcher (pipeline/cron/outbox_dispatcher.go)
-- publishes pending rows and flips status to 1. A cleanup cron deletes
-- published rows older than the retention window. Duplicate publishes after a
-- dispatcher crash are soaked by an in-memory LRU on the consumer keyed on
-- outbox_id (see docs/superpowers/specs/2026-06-11-trade-consistency-and-idempotency-design.md).
CREATE TABLE trade_outbox (
    outbox_id     BIGINT PRIMARY KEY,
    stream_name   VARCHAR(64) NOT NULL,
    payload_json  JSONB NOT NULL,
    status        SMALLINT NOT NULL DEFAULT 0,
    created_at    BIGINT NOT NULL,
    published_at  BIGINT,

    CONSTRAINT chk_trade_outbox_status CHECK (status IN (0, 1))
);
CREATE INDEX idx_trade_outbox_pending
    ON trade_outbox(outbox_id)
    WHERE status = 0;
CREATE INDEX idx_trade_outbox_cleanup
    ON trade_outbox(published_at)
    WHERE status = 1;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS trade_outbox;
DROP TABLE IF EXISTS trade_transfer_receipts;
DROP TABLE IF EXISTS trade_order_events;
DROP TABLE IF EXISTS trade_orders;
DROP TABLE IF EXISTS trading_service_stats_daily;
DROP TABLE IF EXISTS trading_service_stats;
DROP TABLE IF EXISTS trading_services;
-- +goose StatementEnd
