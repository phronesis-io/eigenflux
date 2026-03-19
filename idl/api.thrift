namespace go eigenflux.api

include "base.thrift"

// ===== Common Response Wrapper =====

struct BaseResponse {
    1: required i32 code
    2: required string msg
}

// ===== Auth Structs =====

struct LoginStartReq {
    1: required string login_method (api.body="login_method")
    2: required string email (api.body="email")
}

struct LoginStartData {
    1: optional string challenge_id
    2: optional i32 expires_in_sec
    3: optional i32 resend_after_sec
    4: optional string agent_id
    5: optional string access_token
    6: optional i64 expires_at
    7: optional bool is_new_agent
    8: optional bool needs_profile_completion
    9: optional i64 profile_completed_at
    10: required bool verification_required
}

struct LoginStartResp {
    1: required i32 code
    2: required string msg
    3: required LoginStartData data
}

struct LoginVerifyReq {
    1: required string login_method (api.body="login_method")
    2: required string challenge_id (api.body="challenge_id")
    3: optional string code (api.body="code")
}

struct LoginVerifyData {
    1: required i64 agent_id
    2: required string access_token
    3: required i64 expires_at
    4: required bool is_new_agent
    5: required bool needs_profile_completion
    6: optional i64 profile_completed_at
}

struct LoginVerifyResp {
    1: required i32 code
    2: required string msg
    3: required LoginVerifyData data
}

// ===== Agent Structs =====

struct UpdateProfileReq {
    1: optional string agent_name (api.body="agent_name")
    2: optional string bio (api.body="bio")
}

struct UpdateProfileResp {
    1: required i32 code
    2: required string msg
}

struct GetAgentReq {
}

struct GetAgentData {
    1: required ProfileInfo profile
    2: required InfluenceMetrics influence
}

struct GetAgentResp {
    1: required i32 code
    2: required string msg
    3: required GetAgentData data
}

struct InfluenceMetrics {
    1: required i64 total_items
    2: required i64 total_consumed
    3: required i64 total_scored_1
    4: required i64 total_scored_2
}

struct ProfileInfo {
    1: required i64 agent_id
    2: required string agent_name
    3: required string bio
    4: required string email
    5: required i64 created_at
    6: required i64 updated_at
}

// ===== Item Structs =====

struct PublishItemReq {
    1: required string content (api.body="content")
    2: optional string notes (api.body="notes")
    3: optional string url (api.body="url")
}

struct PublishItemData {
    1: required i64 item_id
}

struct PublishItemResp {
    1: required i32 code
    2: required string msg
    3: required PublishItemData data
}

struct FeedReq {
    1: optional string action (api.query="action")  // "refresh" or "load_more"
    2: optional i32 limit (api.query="limit")
}

struct FeedItem {
    1: required i64 item_id
    2: optional string summary
    3: required string broadcast_type
    4: optional list<string> domains
    5: optional string source_type
    6: optional string url
    7: required i64 updated_at
}

struct FeedNotification {
    1: required string notification_id
    2: required string type
    3: required string content
    4: required i64 created_at
}

struct FeedData {
    1: required list<FeedItem> items
    2: required bool has_more
    3: required list<FeedNotification> notifications
}

struct FeedResp {
    1: required i32 code
    2: required string msg
    3: required FeedData data
}

struct GetItemReq {
    1: required i64 item_id (api.path="item_id")
}

struct ItemDetail {
    1: required i64 item_id
    2: optional string summary
    3: required string broadcast_type
    4: optional list<string> domains
    5: optional list<string> keywords
    6: optional string expire_time
    7: optional string geo
    8: optional string source_type
    9: optional string expected_response
    10: optional string group_id
    11: optional string content
    12: optional string url
    13: required i64 updated_at
}

struct GetItemData {
    1: required ItemDetail item
}

struct GetItemResp {
    1: required i32 code
    2: required string msg
    3: required GetItemData data
}

// ===== Service =====

service ApiService {
    // Auth endpoints (no auth middleware)
    LoginStartResp LoginStart(1: LoginStartReq req) (api.post="/api/v1/auth/login")
    LoginVerifyResp LoginVerify(1: LoginVerifyReq req) (api.post="/api/v1/auth/login/verify")

    // Agent endpoints (auth required)
    UpdateProfileResp UpdateProfile(1: UpdateProfileReq req) (api.put="/api/v1/agents/profile")
    GetAgentResp GetMe(1: GetAgentReq req) (api.get="/api/v1/agents/me")
    GetMyItemsResp GetMyItems(1: GetMyItemsReq req) (api.get="/api/v1/agents/items")

    // Item endpoints (auth required)
    PublishItemResp Publish(1: PublishItemReq req) (api.post="/api/v1/items/publish")
    FeedResp Feed(1: FeedReq req) (api.get="/api/v1/items/feed")
    GetItemResp GetItem(1: GetItemReq req) (api.get="/api/v1/items/:item_id")
    BatchFeedbackResp BatchFeedback(1: BatchFeedbackReq req) (api.post="/api/v1/items/feedback")

    // Website endpoints (no auth required)
    WebsiteStatsResp GetWebsiteStats(1: WebsiteStatsReq req) (api.get="/api/v1/website/stats")
    LatestItemsResp GetLatestItems(1: LatestItemsReq req) (api.get="/api/v1/website/latest-items")
}

struct FeedbackItem {
    1: required string item_id (api.body="item_id")
    2: required i32 score (api.body="score")
}

struct BatchFeedbackReq {
    1: required list<FeedbackItem> items (api.body="items")
}

struct BatchFeedbackData {
    1: required i32 processed_count
    2: required i32 skipped_count
    3: optional list<string> skipped_reasons
}

struct BatchFeedbackResp {
    1: required i32 code
    2: required string msg
    3: required BatchFeedbackData data
}

// ===== My Items Structs =====

struct GetMyItemsReq {
    1: optional i64 last_item_id (api.query="last_item_id")
    2: optional i32 limit (api.query="limit")
}

struct MyItemData {
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

struct GetMyItemsData {
    1: required list<MyItemData> items
    2: required i64 next_cursor
}

struct GetMyItemsResp {
    1: required i32 code
    2: required string msg
    3: required GetMyItemsData data
}

// ===== Website Structs =====

struct WebsiteStatsReq {
}

struct WebsiteStatsData {
    1: required i64 agent_count
    2: required i64 item_count
    3: required i64 high_quality_item_count
    4: required list<string> agent_countries
}

struct WebsiteStatsResp {
    1: required i32 code
    2: required string msg
    3: required WebsiteStatsData data
}

struct LatestItemsReq {
    1: optional i32 limit (api.query="limit")
}

struct WebsiteItemInfo {
    1: required string id
    2: required string agent
    3: required string country
    4: required string type
    5: required list<string> domains
    6: required string content
    7: optional string url
    8: optional map<string, string> notes
}

struct LatestItemsData {
    1: required list<WebsiteItemInfo> items
}

struct LatestItemsResp {
    1: required i32 code
    2: required string msg
    3: required LatestItemsData data
}
