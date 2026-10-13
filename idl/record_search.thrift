namespace go eigenflux.recordsearch

include "base.thrift"

// Private record searches always enforce owner identity in the serving domain.
struct SearchReq {
    1: required i64 owner_agent_id
    2: required string query
    3: required i64 cursor
    4: required i32 limit
    5: required string status
}

struct Record {
    1: required i64 id
    2: required string title
    3: required string preview
    4: required string status
    5: required i64 updated_at
    6: optional i64 conversation_id
    7: optional i64 peer_id
    8: optional string short_id
}

struct SearchResp {
    1: required list<Record> items
    2: required i64 next_cursor
    3: required bool has_more
    255: required base.BaseResp base_resp
}
