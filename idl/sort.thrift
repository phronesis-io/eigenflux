namespace go eigenflux.sort

include "base.thrift"
include "trade.thrift"

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
    // entry_type discriminates between candidate kinds in mixed feeds.
    // Absent or "item" → regular item (legacy behaviour). "service" → trading service.
    5: optional string entry_type
}

struct SortItemsResp {
    // item_ids carries ALL ranked IDs in order. After the service-mix feature
    // landed, this list may include service IDs alongside item IDs; consult
    // the parallel sorted_items[i].entry_type to disambiguate.
    1: required list<i64> item_ids
    2: required i64 next_cursor
    3: optional list<SortedItem> sorted_items
    255: required base.BaseResp base_resp
}

struct SubIntent {
    1: required string name
    2: required string query_text
    3: optional double importance
}

struct SearchFilters {
    1: optional i64 max_price_atomic
    2: optional i64 deadline_ms_max
}

struct SearchServicesReq {
    1: required string raw_query
    2: optional list<SubIntent> sub_intents
    3: optional i32 limit
    4: optional SearchFilters filters
}

struct SearchedService {
    1: required i64 service_id
    2: required string title
    3: required i64 seller_agent_id
    4: required i64 amount_atomic
    5: required string asset
    6: required i64 delivery_deadline_ms
    7: required double score
    8: required list<string> matched_intents
    9: required string winning_intent
    10: required map<string, double> score_breakdown
    11: required map<string, double> stats
}

struct SearchServicesDebug {
    1: required string sub_intents_source
    2: required list<SubIntent> effective_sub_intents
}

struct SearchServicesResp {
    1: required list<SearchedService> results
    2: required SearchServicesDebug debug
    255: required base.BaseResp base_resp
}

service SortService {
    SortItemsResp SortItems(1: SortItemsReq req)
    SearchServicesResp SearchServices(1: SearchServicesReq req)
}
