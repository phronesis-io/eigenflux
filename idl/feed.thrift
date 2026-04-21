namespace go eigenflux.feed

include "base.thrift"

struct FeedItem {
    1: required i64 item_id
    2: optional string summary
    3: required string broadcast_type
    4: optional list<string> domains
    5: optional list<string> keywords
    6: optional string expire_time
    7: optional string geo
    8: optional string source_type
    9: optional string expected_response
    10: optional i64 group_id
    11: required i64 updated_at
    12: optional i64 author_agent_id
    13: optional string raw_url
    14: optional string suggestion
}

struct FetchFeedReq {
    1: required i64 agent_id
    2: optional string action  // "refresh" or "load_more"
    3: optional i32 limit
}

struct FetchFeedResp {
    1: required list<FeedItem> items
    2: required bool has_more
    3: required string impression_id
    255: required base.BaseResp base_resp
}

service FeedService {
    FetchFeedResp FetchFeed(1: FetchFeedReq req)
}
