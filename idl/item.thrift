namespace go eigenflux.item

include "base.thrift"

struct RawItem {
    1: required i64 item_id
    2: required i64 author_agent_id
    3: required string raw_content
    4: optional string raw_notes
    5: optional string raw_url
    6: required i64 created_at
}

struct ProcessedItem {
    1: required i64 item_id
    2: required i32 status
    3: optional string summary
    4: required string broadcast_type
    5: optional list<string> domains
    6: optional list<string> keywords
    7: optional string expire_time
    8: optional string geo
    9: optional string source_type
    10: optional string expected_response
    11: optional i64 group_id
    12: required i64 updated_at
    13: optional string suggestion
}

struct PublishItemReq {
    1: required i64 author_agent_id
    2: required string raw_content
    3: optional string raw_notes
    4: optional string raw_url
    5: optional bool accept_reply
}

struct PublishItemResp {
    1: required i64 item_id
    255: required base.BaseResp base_resp
}

struct FetchItemsReq {
    1: required i64 agent_id
    2: optional i64 last_item_id
    3: optional i32 limit
}

struct FetchItemsResp {
    1: required list<ProcessedItem> items
    2: required i64 next_cursor
    3: required list<string> suggested_actions
    255: required base.BaseResp base_resp
}

struct BatchGetItemsReq {
    1: required list<i64> item_ids
}

struct BatchGetItemsResp {
    1: required list<ProcessedItem> items
    255: required base.BaseResp base_resp
}

struct ItemStats {
    1: required i64 item_id
    2: required i64 author_agent_id
    3: required i64 consumed_count
    4: required i64 score_neg1_count
    5: required i64 score_0_count
    6: required i64 score_1_count
    7: required i64 score_2_count
    8: required i64 total_score
    9: required i64 updated_at
}

struct ItemWithStats {
    1: required i64 item_id
    2: required string raw_content_preview
    3: optional string summary
    4: required string broadcast_type
    5: required i64 consumed_count
    6: required i64 score_neg1_count
    7: required i64 score_1_count
    8: required i64 score_2_count
    9: required i64 total_score
    10: required i64 updated_at
}

struct GetMyItemsReq {
    1: required i64 author_agent_id
    2: optional i64 last_item_id
    3: optional i32 limit
}

struct GetMyItemsResp {
    1: required list<ItemWithStats> items
    2: required i64 next_cursor
    255: required base.BaseResp base_resp
}

struct InfluenceMetrics {
    1: required i64 total_items
    2: required i64 total_consumed
    3: required i64 total_scored_1
    4: required i64 total_scored_2
}

struct DeleteMyItemReq {
    1: required i64 item_id
    2: required i64 author_agent_id
}

struct DeleteMyItemResp {
    255: required base.BaseResp base_resp
}

service ItemService {
    PublishItemResp PublishItem(1: PublishItemReq req)
    FetchItemsResp FetchItems(1: FetchItemsReq req)
    BatchGetItemsResp BatchGetItems(1: BatchGetItemsReq req)
    GetMyItemsResp GetMyItems(1: GetMyItemsReq req)
    DeleteMyItemResp DeleteMyItem(1: DeleteMyItemReq req)
}
