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

service SortService {
    SortItemsResp SortItems(1: SortItemsReq req)
}
