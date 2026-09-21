namespace go eigenflux.sort

include "base.thrift"
struct SortItemsReq {
    1: required i64 agent_id
    2: optional i64 last_updated_at
    3: optional i32 limit
}

struct SortedItem {
    1: required i64 item_id
    2: required double score
    3: optional string agent_features
    4: optional string item_features
}

struct SortItemsResp {
    1: required list<i64> item_ids
    2: required i64 next_cursor
    3: optional list<SortedItem> sorted_items
    255: required base.BaseResp base_resp
}

struct CommissionSearchFilters {
    1: optional i64 min_price_fen
    2: optional i64 max_price_fen
    3: optional i64 min_promised_delivery_ms
    4: optional i64 max_promised_delivery_ms
}

struct CommissionCandidate {
    1: required i64 commission_id
    2: required double score
    3: optional string features
}

struct SearchCommissionsReq {
    1: required string query
    2: optional CommissionSearchFilters filters
    3: optional i32 limit
    4: optional i64 commission_id
}

struct SearchCommissionsResp {
    1: required list<CommissionCandidate> candidates
    255: required base.BaseResp base_resp
}

struct RecommendCommissionsReq {
    1: required i64 agent_id
    2: optional CommissionSearchFilters filters
    3: optional i32 limit
}

struct RecommendCommissionsResp {
    1: required list<CommissionCandidate> candidates
    255: required base.BaseResp base_resp
}

// Payloads are versioned strict JSON contracts in pkg/discovery. Ownership and
// operation remain typed, authenticated RPC fields rather than payload fields.
struct DiscoveryReq {
    1: required i64 agent_id
    2: required string operation
    3: required string payload
    4: optional string idempotency_key
    5: optional i64 resource_id
}
struct DiscoveryResp {
    1: required string payload
    255: required base.BaseResp base_resp
}
service SortService {
    DiscoveryResp Discovery(1: DiscoveryReq req)

    SortItemsResp SortItems(1: SortItemsReq req)
    SearchCommissionsResp SearchCommissions(1: SearchCommissionsReq req)
    RecommendCommissionsResp RecommendCommissions(1: RecommendCommissionsReq req)
}
