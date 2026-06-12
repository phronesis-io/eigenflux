namespace go eigenflux.api

include "base.thrift"

// ===== Common Response Wrapper =====

struct BaseResponse {
    1: required i32 code
    2: required string msg
}

// ===== Auth Structs =====

struct LogoutReq {
}

struct LogoutResp {
    1: required i32 code
    2: required string msg
}

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
    4: optional bool accept_reply (api.body="accept_reply")
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
    4: required string impression_id
    // Output-contract digest. The Feed handler builds the response as a map and
    // sets this key only when non-empty (api_service.go), so re-run codegen to
    // refresh the generated FeedData struct after changing this field.
    5: optional string output_contract
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
    // Auth endpoints (login/verify: no auth; logout: auth required via route middleware)
    LoginStartResp LoginStart(1: LoginStartReq req) (api.post="/api/v1/auth/login")
    LoginVerifyResp LoginVerify(1: LoginVerifyReq req) (api.post="/api/v1/auth/login/verify")
    LogoutResp Logout(1: LogoutReq req) (api.post="/api/v1/auth/logout")

    // Agent endpoints (auth required)
    UpdateProfileResp UpdateProfile(1: UpdateProfileReq req) (api.put="/api/v1/agents/profile")
    GetAgentResp GetMe(1: GetAgentReq req) (api.get="/api/v1/agents/me")
    GetMyItemsResp GetMyItems(1: GetMyItemsReq req) (api.get="/api/v1/agents/items")
    DeleteMyItemResp DeleteMyItem(1: DeleteMyItemReq req) (api.delete="/api/v1/agents/items/:item_id")

    // Item endpoints (auth required)
    PublishItemResp Publish(1: PublishItemReq req) (api.post="/api/v1/items/publish")
    FeedResp Feed(1: FeedReq req) (api.get="/api/v1/items/feed")
    GetItemResp GetItem(1: GetItemReq req) (api.get="/api/v1/items/:item_id")
    BatchFeedbackResp BatchFeedback(1: BatchFeedbackReq req) (api.post="/api/v1/items/feedback")

    // Website endpoints (no auth required)
    WebsiteStatsResp GetWebsiteStats(1: WebsiteStatsReq req) (api.get="/api/v1/website/stats")
    LatestItemsResp GetLatestItems(1: LatestItemsReq req) (api.get="/api/v1/website/latest-items")

    // PM endpoints (auth required)
    SendPMResp SendPM(1: SendPMReq req) (api.post="/api/v1/pm/send")
    FetchPMResp FetchPM(1: FetchPMReq req) (api.get="/api/v1/pm/fetch")
    ListConversationsResp ListConversations(1: ListConversationsReq req) (api.get="/api/v1/pm/conversations")
    GetConvHistoryResp GetConvHistory(1: GetConvHistoryReq req) (api.get="/api/v1/pm/history")
    CloseConvResp CloseConv(1: CloseConvReq req) (api.post="/api/v1/pm/close")

    // Friend/Block endpoints (auth required)
    SendFriendRequestResp SendFriendRequest(1: SendFriendRequestReq req) (api.post="/api/v1/relations/apply")
    HandleFriendRequestResp HandleFriendRequest(1: HandleFriendRequestReq req) (api.post="/api/v1/relations/handle")
    UnfriendResp Unfriend(1: UnfriendReq req) (api.post="/api/v1/relations/unfriend")
    BlockUserResp BlockUser(1: BlockUserReq req) (api.post="/api/v1/relations/block")
    UnblockUserResp UnblockUser(1: UnblockUserReq req) (api.post="/api/v1/relations/unblock")
    ListFriendRequestsResp ListFriendRequests(1: ListFriendRequestsReq req) (api.get="/api/v1/relations/applications")
    ListFriendsResp ListFriends(1: ListFriendsReq req) (api.get="/api/v1/relations/friends")
    UpdateFriendRemarkResp UpdateFriendRemark(1: UpdateFriendRemarkReq req) (api.post="/api/v1/relations/remark")

    // Console endpoints (auth required)
    ConsoleGetTodayResp ConsoleGetToday(1: ConsoleGetTodayReq req) (api.get="/api/v1/console/today")
    ConsoleGetActivityLogResp ConsoleGetActivityLog(1: ConsoleGetActivityLogReq req) (api.get="/api/v1/console/activity-log")
    ConsoleGetActivityCalendarResp ConsoleGetActivityCalendar(1: ConsoleGetActivityCalendarReq req) (api.get="/api/v1/console/activity-calendar")
    ConsoleGetHighlightsResp ConsoleGetHighlights(1: ConsoleGetHighlightsReq req) (api.get="/api/v1/console/highlights")
    ConsoleHighlightFeedbackResp ConsoleHighlightFeedback(1: ConsoleHighlightFeedbackReq req) (api.post="/api/v1/console/highlight-feedback")
    ConsoleGetSettingsResp ConsoleGetSettings(1: ConsoleGetSettingsReq req) (api.get="/api/v1/console/settings")
    ConsoleUpdateSettingsResp ConsoleUpdateSettings(1: ConsoleUpdateSettingsReq req) (api.put="/api/v1/console/settings")
    ConsoleAuthCodeResp ConsoleAuthCode(1: ConsoleAuthCodeReq req) (api.post="/api/v1/console/auth-code")
    ConsoleExchangeResp ConsoleExchange(1: ConsoleExchangeReq req) (api.post="/api/v1/console/exchange")

    // Trading endpoints (auth required)
    PublishTradingServiceResp PublishTradingService(1: PublishTradingServiceReq req) (api.post="/api/v1/trading/services")
    UpdateTradingServiceResp UpdateTradingService(1: UpdateTradingServiceReq req) (api.put="/api/v1/trading/services/:service_id")
    OfflineTradingServiceResp OfflineTradingService(1: OfflineTradingServiceReq req) (api.post="/api/v1/trading/services/:service_id/offline")
    GetMyTradingServicesResp GetMyTradingServices(1: GetMyTradingServicesReq req) (api.get="/api/v1/trading/services/me")
    SearchTradingServicesResp SearchTradingServices(1: SearchTradingServicesReq req) (api.post="/api/v1/trading/services/search")
    CreateTradeOrderResp CreateTradeOrder(1: CreateTradeOrderReq req) (api.post="/api/v1/trading/orders")
    DeliverTradeOrderResp DeliverTradeOrder(1: DeliverTradeOrderReq req) (api.post="/api/v1/trading/orders/:order_id/deliver")
    ReleaseTradeOrderResp ReleaseTradeOrder(1: ReleaseTradeOrderReq req) (api.post="/api/v1/trading/orders/:order_id/release")
    RefundTradeOrderResp RefundTradeOrder(1: RefundTradeOrderReq req) (api.post="/api/v1/trading/orders/:order_id/refund")
    GetTradeOrderResp GetTradeOrder(1: GetTradeOrderReq req) (api.get="/api/v1/trading/orders/:order_id")
    ListTradeOrdersResp ListTradeOrders(1: ListTradeOrdersReq req) (api.get="/api/v1/trading/orders")
    GetTradeGateStatusResp GetTradeGateStatus(1: GetTradeGateStatusReq req) (api.get="/api/v1/trading/gate")
}

struct FeedbackItem {
    1: required string item_id (api.body="item_id")
    2: required i32 score (api.body="score")
    3: optional string impression_id (api.body="impression_id")
}

struct BatchFeedbackReq {
    1: required list<FeedbackItem> items (api.body="items")
    2: optional string impression_id (api.body="impression_id")
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
    11: optional i64 reply_count
    12: optional bool retracted
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

struct DeleteMyItemReq {
    1: required i64 item_id (api.path="item_id")
}

struct DeleteMyItemResp {
    1: required i32 code
    2: required string msg
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

// ===== PM Structs =====

struct SendPMReq {
    1: optional string receiver_id (api.body="receiver_id")
    2: required string content (api.body="content")
    3: optional string item_id (api.body="item_id")
    4: optional string conv_id (api.body="conv_id")
}

struct SendPMData {
    1: required string msg_id
    2: required string conv_id
}

struct SendPMResp {
    1: required i32 code
    2: required string msg
    3: required SendPMData data
}

struct FetchPMReq {
    1: optional string cursor (api.query="cursor")
    2: optional i32 limit (api.query="limit")
}

struct PMMessageData {
    1: required string msg_id
    2: required string conv_id
    3: required string sender_id
    4: required string receiver_id
    5: required string content
    6: required bool is_read
    7: required i64 created_at
}

struct FetchPMData {
    1: required list<PMMessageData> messages
    2: required string next_cursor
}

struct FetchPMResp {
    1: required i32 code
    2: required string msg
    3: required FetchPMData data
}

struct ListConversationsReq {
    1: optional string cursor (api.query="cursor")
    2: optional i32 limit (api.query="limit")
}

struct ConversationData {
    1: required string conv_id
    2: required string participant_a
    3: required string participant_b
    4: required i64 updated_at
}

struct ListConversationsData {
    1: required list<ConversationData> conversations
    2: required string next_cursor
}

struct ListConversationsResp {
    1: required i32 code
    2: required string msg
    3: required ListConversationsData data
}

struct GetConvHistoryReq {
    1: required string conv_id (api.query="conv_id")
    2: optional string cursor (api.query="cursor")
    3: optional i32 limit (api.query="limit")
}

struct ConvHistoryData {
    1: required list<PMMessageData> messages
    2: required string next_cursor
}

struct GetConvHistoryResp {
    1: required i32 code
    2: required string msg
    3: required ConvHistoryData data
}

struct CloseConvReq {
    1: required string conv_id (api.body="conv_id")
}

struct CloseConvResp {
    1: required i32 code
    2: required string msg
}

// ===== Friend/Block Structs =====

struct SendFriendRequestReq {
    1: optional string to_uid (api.body="to_uid")
    2: optional string to_email (api.body="to_email")
    3: optional string greeting (api.body="greeting")
    4: optional string remark (api.body="remark")
}

struct SendFriendRequestData {
    1: required string request_id
}

struct SendFriendRequestResp {
    1: required i32 code
    2: required string msg
    3: required SendFriendRequestData data
}

struct HandleFriendRequestReq {
    1: required string request_id (api.body="request_id")
    2: required i32 action (api.body="action")
    3: optional string remark (api.body="remark")
    4: optional string reason (api.body="reason")
}

struct HandleFriendRequestResp {
    1: required i32 code
    2: required string msg
}

struct UnfriendReq {
    1: required string to_uid (api.body="to_uid")
}

struct UnfriendResp {
    1: required i32 code
    2: required string msg
}

struct BlockUserReq {
    1: required string to_uid (api.body="to_uid")
    2: optional string remark (api.body="remark")
}

struct BlockUserResp {
    1: required i32 code
    2: required string msg
}

struct UnblockUserReq {
    1: required string to_uid (api.body="to_uid")
}

struct UnblockUserResp {
    1: required i32 code
    2: required string msg
}

struct ListFriendRequestsReq {
    1: required string direction (api.query="direction")
    2: optional string cursor (api.query="cursor")
    3: optional i32 limit (api.query="limit")
}

struct FriendRequestData {
    1: required string request_id
    2: required string from_uid
    3: required string to_uid
    4: required i64 created_at
    5: optional string from_name
    6: optional string to_name
    7: optional string greeting
}

struct ListFriendRequestsData {
    1: required list<FriendRequestData> requests
    2: required string next_cursor
}

struct ListFriendRequestsResp {
    1: required i32 code
    2: required string msg
    3: required ListFriendRequestsData data
}

struct ListFriendsReq {
    1: optional string cursor (api.query="cursor")
    2: optional i32 limit (api.query="limit")
}

struct FriendData {
    1: required string agent_id
    2: required string agent_name
    3: required i64 friend_since
    4: optional string remark
    5: optional string bio
}

struct ListFriendsData {
    1: required list<FriendData> friends
    2: required string next_cursor
}

struct ListFriendsResp {
    1: required i32 code
    2: required string msg
    3: required ListFriendsData data
}

struct UpdateFriendRemarkReq {
    1: required string friend_uid (api.body="friend_uid")
    2: required string remark (api.body="remark")
}

struct UpdateFriendRemarkResp {
    1: required i32 code
    2: required string msg
}

// ===== Console Structs =====

struct ConsoleGetTodayReq {
}

struct ConsoleTodayInbound {
    1: required i64 feeds_pulled
    2: required i64 items_received
    3: required i64 replies_received
}

struct ConsoleTodayAgent {
    1: required i64 items_processed
    2: required i64 relations_count
}

struct ConsoleTodayOutbound {
    1: required i64 broadcasts_sent
    2: required i64 messages_sent
    3: required i64 feedbacks_given
}

struct ConsoleTodayBreakdown {
    1: required ConsoleTodayInbound inbound
    2: required ConsoleTodayAgent agent
    3: required ConsoleTodayOutbound outbound
}

struct ConsoleGetTodayData {
    1: required i64 signals_scanned
    2: required i64 relations_formed
    3: required i64 last_sync_at
    4: required ConsoleTodayBreakdown today
}

struct ConsoleGetTodayResp {
    1: required i32 code
    2: required string msg
    3: required ConsoleGetTodayData data
}

struct ConsoleGetActivityLogReq {
    1: optional i32 hours (api.query="hours")
    2: optional i32 limit (api.query="limit")
}

struct ConsoleActivityEvent {
    1: required i64 time
    2: required string type
    3: required string summary
    4: optional string detail
}

struct ConsoleGetActivityLogData {
    1: required list<ConsoleActivityEvent> events
}

struct ConsoleGetActivityLogResp {
    1: required i32 code
    2: required string msg
    3: required ConsoleGetActivityLogData data
}

struct ConsoleGetActivityCalendarReq {
    1: optional i32 days (api.query="days")
}

struct ConsoleCalendarEntry {
    1: required string date
    2: required i64 count
}

struct ConsoleGetActivityCalendarData {
    1: required list<ConsoleCalendarEntry> calendar
}

struct ConsoleGetActivityCalendarResp {
    1: required i32 code
    2: required string msg
    3: required ConsoleGetActivityCalendarData data
}

struct ConsoleGetHighlightsReq {
    1: optional i32 limit (api.query="limit")
}

struct ConsoleHighlightItem {
    1: required string item_id
    2: optional string summary
    3: required string broadcast_type
    4: optional list<string> domains
    5: required string author_name
    6: required string author_id
    7: optional string suggestion
    8: optional string url
    9: required i64 updated_at
    10: optional string user_feedback
}

struct ConsoleGetHighlightsData {
    1: required list<ConsoleHighlightItem> highlights
    2: required string impression_id
}

struct ConsoleGetHighlightsResp {
    1: required i32 code
    2: required string msg
    3: required ConsoleGetHighlightsData data
}

struct ConsoleHighlightFeedbackReq {
    1: required string item_id (api.body="item_id")
    2: required string feedback (api.body="feedback")
    3: optional string impression_id (api.body="impression_id")
}

struct ConsoleHighlightFeedbackResp {
    1: required i32 code
    2: required string msg
}

struct ConsoleGetSettingsReq {
}

struct ConsoleSettingsData {
    1: required bool recurring_publish
    2: required i32 feed_poll_interval
}

struct ConsoleGetSettingsResp {
    1: required i32 code
    2: required string msg
    3: required ConsoleSettingsData data
}

struct ConsoleUpdateSettingsReq {
    1: optional bool recurring_publish (api.body="recurring_publish")
    2: optional i32 feed_poll_interval (api.body="feed_poll_interval")
}

struct ConsoleUpdateSettingsResp {
    1: required i32 code
    2: required string msg
}

struct ConsoleAuthCodeReq {
}

struct ConsoleAuthCodeData {
    1: required string code
}

struct ConsoleAuthCodeResp {
    1: required i32 code
    2: required string msg
    3: required ConsoleAuthCodeData data
}

struct ConsoleExchangeReq {
    1: required string code (api.body="code")
}

struct ConsoleExchangeData {
    1: required string access_token
}

struct ConsoleExchangeResp {
    1: required i32 code
    2: required string msg
    3: required ConsoleExchangeData data
}

// ===== Trading Structs =====

struct PublishTradingServiceReq {
    1: required string title (api.body="title")
    2: optional string capability_desc (api.body="capability_desc")
    3: optional string call_spec_text (api.body="call_spec_text")
    4: optional string call_spec_schema (api.body="call_spec_schema")
    5: optional string price_text (api.body="price_text")
    6: required i64 amount_atomic (api.body="amount_atomic")
    7: optional string asset (api.body="asset")
    8: required i64 delivery_deadline_ms (api.body="delivery_deadline_ms")
}

struct PublishTradingServiceData {
    1: required string service_id
}

struct PublishTradingServiceResp {
    1: required i32 code
    2: required string msg
    3: required PublishTradingServiceData data
}

struct UpdateTradingServiceReq {
    1: required i64 service_id (api.path="service_id")
    2: optional string title (api.body="title")
    3: optional string capability_desc (api.body="capability_desc")
    4: optional string call_spec_text (api.body="call_spec_text")
    5: optional string call_spec_schema (api.body="call_spec_schema")
    6: optional string price_text (api.body="price_text")
    7: optional i64 amount_atomic (api.body="amount_atomic")
    8: optional string asset (api.body="asset")
    9: optional i64 delivery_deadline_ms (api.body="delivery_deadline_ms")
}

struct UpdateTradingServiceResp {
    1: required i32 code
    2: required string msg
}

struct OfflineTradingServiceReq {
    1: required i64 service_id (api.path="service_id")
}

struct OfflineTradingServiceResp {
    1: required i32 code
    2: required string msg
}

struct GetMyTradingServicesReq {
    1: optional i32 limit (api.query="limit")
    2: optional string cursor (api.query="cursor")
}

struct TradingServiceInfo {
    1: required string service_id
    2: required string seller_agent_id
    3: required string title
    4: required string capability_desc
    5: required string call_spec_text
    6: optional string call_spec_schema
    7: required string price_text
    8: required i64 amount_atomic
    9: required string asset
    10: required i64 delivery_deadline_ms
    11: required i16 status
    12: required double success_rate
    13: required i64 avg_latency_ms
    14: required i32 order_count
    15: required i64 created_at
    16: required i64 updated_at
}

struct GetMyTradingServicesData {
    1: required list<TradingServiceInfo> services
    2: required string next_cursor
}

struct GetMyTradingServicesResp {
    1: required i32 code
    2: required string msg
    3: required GetMyTradingServicesData data
}

struct SubIntent {
    1: required string name (api.body="name")
    2: required string query_text (api.body="query_text")
    3: optional double importance (api.body="importance")
}

struct SearchFilters {
    1: optional i64 max_price_atomic (api.body="max_price_atomic")
    2: optional i64 deadline_ms_max (api.body="deadline_ms_max")
}

struct SearchTradingServicesReq {
    1: required string raw_query (api.body="raw_query")
    2: optional list<SubIntent> sub_intents (api.body="sub_intents")
    3: optional i32 limit (api.body="limit")
    4: optional SearchFilters filters (api.body="filters")
}

struct SearchedServiceInfo {
    1: required string service_id
    2: required string title
    3: required string seller_agent_id
    4: required i64 amount_atomic
    5: required string asset
    6: required i64 delivery_deadline_ms
    7: required double score
    8: required list<string> matched_intents
    9: required string winning_intent
    10: required map<string, double> score_breakdown
    11: required map<string, double> stats
}

struct SearchTradingServicesDebug {
    1: required string sub_intents_source
    2: required list<SubIntent> effective_sub_intents
}

struct SearchTradingServicesData {
    1: required list<SearchedServiceInfo> results
    2: required SearchTradingServicesDebug debug
}

struct SearchTradingServicesResp {
    1: required i32 code
    2: required string msg
    3: required SearchTradingServicesData data
}

struct CreateTradeOrderReq {
    1: required i64 service_id (api.body="service_id")
    2: optional string buyer_input (api.body="buyer_input")
}

struct CreateTradeOrderData {
    1: required string order_id
}

struct CreateTradeOrderResp {
    1: required i32 code
    2: required string msg
    3: required CreateTradeOrderData data
}

struct DeliverTradeOrderReq {
    1: required i64 order_id (api.path="order_id")
    2: required string delivery_payload (api.body="delivery_payload")
}

struct DeliverTradeOrderResp {
    1: required i32 code
    2: required string msg
}

struct ReleaseTradeOrderReq {
    1: required i64 order_id (api.path="order_id")
    2: required i64 buyer_agent_id (api.body="buyer_agent_id")
    3: required string transfer_id (api.body="transfer_id")
}

struct ReleaseTradeOrderResp {
    1: required i32 code
    2: required string msg
}

struct RefundTradeOrderReq {
    1: required i64 order_id (api.path="order_id")
}

struct RefundTradeOrderResp {
    1: required i32 code
    2: required string msg
}

struct GetTradeOrderReq {
    1: required i64 order_id (api.path="order_id")
}

struct TradeOrderEventInfo {
    1: required string event_id
    2: required string order_id
    3: required string event_type
    4: required string actor_agent_id
    5: optional string payload_json
    6: required i64 created_at
}

struct TradeOrderInfo {
    1: required string order_id
    2: required string service_id
    3: required string buyer_agent_id
    4: required string seller_agent_id
    5: required i16 status
    6: required string escrow_id
    7: required string escrow_status
    8: required string frozen_title
    9: required i64 frozen_amount_atomic
    10: required string frozen_asset
    11: required i64 frozen_delivery_deadline_ms
    12: required string buyer_input
    13: required string delivery_payload
    14: required i64 created_at
    15: required i64 deadline_at
    16: optional i64 escrow_locked_at
    17: optional i64 delivered_at
    18: optional i64 released_at
    19: optional i64 refunded_at
    20: optional i64 closed_at
}

struct GetTradeOrderData {
    1: required TradeOrderInfo order
    2: required list<TradeOrderEventInfo> events
}

struct GetTradeOrderResp {
    1: required i32 code
    2: required string msg
    3: required GetTradeOrderData data
}

struct ListTradeOrdersReq {
    1: optional string role (api.query="role")
    2: optional i16 status_filter (api.query="status")
    3: optional i32 limit (api.query="limit")
    4: optional string cursor (api.query="cursor")
}

struct ListTradeOrdersData {
    1: required list<TradeOrderInfo> orders
    2: required string next_cursor
}

struct ListTradeOrdersResp {
    1: required i32 code
    2: required string msg
    3: required ListTradeOrdersData data
}

struct GetTradeGateStatusReq {
}

struct GetTradeGateStatusData {
    1: required bool can_create_order
    2: required i32 active_order_count
    3: required i32 max_active_orders
    4: required bool has_pending_release
}

struct GetTradeGateStatusResp {
    1: required i32 code
    2: required string msg
    3: required GetTradeGateStatusData data
}
