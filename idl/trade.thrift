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
    20: list<string> capability_tags
    21: string use_cases
    22: i32 enrichment_version
}

struct TradeOrder {
    1: i64 order_id
    2: i64 service_id
    3: i64 buyer_agent_id
    4: i64 seller_agent_id
    5: i16 status
    6: string transfer_id
    7: string transfer_state
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
    18: i64 paid_at
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

struct CreateOrderReq {
    1: i64 buyer_agent_id
    2: i64 service_id
    3: string buyer_input
    4: string idempotency_key   // optional; empty = skip idempotency check
}

struct CreateOrderResp {
    1: i64 order_id
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
    3: string transfer_id
}

struct ReleaseOrderResp {
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

struct GetGateStatusReq {
    1: i64 buyer_agent_id
}

struct GetGateStatusResp {
    1: bool can_create_order
    2: i32 active_order_count
    3: i32 max_active_orders
    4: bool has_pending_release
    5: i32 unpaid_order_count
    255: required base.BaseResp base_resp
}

service TradeService {
    PublishServiceResp PublishService(1: PublishServiceReq req)
    UpdateServiceResp UpdateService(1: UpdateServiceReq req)
    OfflineServiceResp OfflineService(1: OfflineServiceReq req)
    GetMyServicesResp GetMyServices(1: GetMyServicesReq req)

    CreateOrderResp CreateOrder(1: CreateOrderReq req)
    DeliverOrderResp DeliverOrder(1: DeliverOrderReq req)
    ReleaseOrderResp ReleaseOrder(1: ReleaseOrderReq req)
    GetOrderResp GetOrder(1: GetOrderReq req)
    ListOrdersResp ListOrders(1: ListOrdersReq req)

    GetGateStatusResp GetGateStatus(1: GetGateStatusReq req)
}
