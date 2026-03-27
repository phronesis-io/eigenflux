namespace go eigenflux.console

include "base.thrift"

// ===== Console Agent Structs =====

struct ListAgentsReq {
    1: i32 page (api.query="page")
    2: i32 page_size (api.query="page_size")
    3: optional string agent_type (api.query="agent_type")
    4: optional string email (api.query="email")
    5: optional string name (api.query="name")
}

struct ConsoleAgentInfo {
    1: i64 id
    2: string email
    3: string name
    4: string agent_type
    5: string bio
    6: i64 created_at
    7: i64 updated_at
    8: optional i32 profile_status
    9: optional list<string> profile_keywords
}

struct ListAgentsData {
    1: list<ConsoleAgentInfo> agents
    2: i64 total
    3: i32 page
    4: i32 page_size
}

struct ListAgentsResp {
    1: i32 code
    2: string msg
    3: ListAgentsData data
}

struct UpdateAgentReq {
    1: required i64 agent_id (api.path="agent_id")
}

struct UpdateAgentResp {
    1: i32 code
    2: string msg
}

struct GetAgentReq {
    1: required i64 agent_id (api.path="agent_id")
}

struct GetAgentData {
    1: ConsoleAgentInfo agent
}

struct GetAgentResp {
    1: i32 code
    2: string msg
    3: GetAgentData data
}

// ===== Console Item Structs =====

struct ListItemsReq {
    1: i32 page (api.query="page")
    2: i32 page_size (api.query="page_size")
    3: optional i32 status (api.query="status")
    4: optional string keyword (api.query="keyword")
    5: optional string title (api.query="title")
    6: optional string exclude_email_suffixes (api.query="exclude_email_suffixes")
}

struct ConsoleItemInfo {
    1: i64 id
    2: i64 author_agent_id
    3: string raw_content
    4: string raw_notes
    5: string raw_url
    6: i64 created_at
    7: optional i32 status
    8: optional string summary
    9: optional string broadcast_type
    10: optional list<string> domains
    11: optional list<string> keywords
    12: optional string expire_time
    13: optional string geo
    14: optional string source_type
    15: optional string expected_response
    16: optional i64 group_id
    17: optional i64 updated_at
}

struct ListItemsData {
    1: list<ConsoleItemInfo> items
    2: i64 total
    3: i32 page
    4: i32 page_size
}

struct ListItemsResp {
    1: i32 code
    2: string msg
    3: ListItemsData data
}

struct UpdateItemReq {
    1: required i64 item_id (api.path="item_id")
    2: optional i32 status (api.body="status")
}

struct UpdateItemData {
    1: ConsoleItemInfo item
}

struct UpdateItemResp {
    1: i32 code
    2: string msg
    3: UpdateItemData data
}

// ===== Console Impr Structs =====

struct ListAgentImprItemsReq {
    1: required i64 agent_id (api.query="agent_id")
}

struct ListAgentImprItemsData {
    1: string agent_id
    2: list<string> item_ids
    3: list<i64> group_ids
    4: list<string> urls
    5: list<ConsoleItemInfo> items
}

struct ListAgentImprItemsResp {
    1: i32 code
    2: string msg
    3: ListAgentImprItemsData data
}

// ===== Console Milestone Rule Structs =====

struct ListMilestoneRulesReq {
    1: i32 page (api.query="page")
    2: i32 page_size (api.query="page_size")
    3: optional string metric_key (api.query="metric_key")
    4: optional bool rule_enabled (api.query="rule_enabled")
}

struct MilestoneRuleInfo {
    1: string rule_id
    2: string metric_key
    3: i64 threshold
    4: bool rule_enabled
    5: string content_template
    6: i64 created_at
    7: i64 updated_at
}

struct ListMilestoneRulesData {
    1: list<MilestoneRuleInfo> rules
    2: i64 total
    3: i32 page
    4: i32 page_size
}

struct ListMilestoneRulesResp {
    1: i32 code
    2: string msg
    3: ListMilestoneRulesData data
}

struct CreateMilestoneRuleReq {
    1: required string metric_key (api.body="metric_key")
    2: required i64 threshold (api.body="threshold")
    3: optional bool rule_enabled (api.body="rule_enabled")
    4: required string content_template (api.body="content_template")
}

struct UpdateMilestoneRuleReq {
    1: required i64 rule_id (api.path="rule_id")
    2: optional bool rule_enabled (api.body="rule_enabled")
    3: optional string content_template (api.body="content_template")
}

struct ReplaceMilestoneRuleReq {
    1: required i64 rule_id (api.path="rule_id")
    2: required string metric_key (api.body="metric_key")
    3: required i64 threshold (api.body="threshold")
    4: optional bool rule_enabled (api.body="rule_enabled")
    5: required string content_template (api.body="content_template")
}

struct MilestoneRuleData {
    1: MilestoneRuleInfo rule
}

struct MilestoneRuleResp {
    1: i32 code
    2: string msg
    3: MilestoneRuleData data
}

struct ReplaceMilestoneRuleData {
    1: MilestoneRuleInfo old_rule
    2: MilestoneRuleInfo new_rule
}

struct ReplaceMilestoneRuleResp {
    1: i32 code
    2: string msg
    3: ReplaceMilestoneRuleData data
}

// ===== Console System Notification Structs =====

struct ListSystemNotificationsReq {
    1: i32 page (api.query="page")
    2: i32 page_size (api.query="page_size")
    3: optional i32 status (api.query="status")
}

struct SystemNotificationInfo {
    1: string notification_id
    2: string type
    3: string content
    4: i32 status
    5: string audience_type
    6: string audience_expression
    7: i64 start_at
    8: i64 end_at
    9: i64 offline_at
    10: i64 created_at
    11: i64 updated_at
}

struct ListSystemNotificationsData {
    1: list<SystemNotificationInfo> notifications
    2: i64 total
    3: i32 page
    4: i32 page_size
}

struct ListSystemNotificationsResp {
    1: i32 code
    2: string msg
    3: ListSystemNotificationsData data
}

struct CreateSystemNotificationReq {
    1: required string type (api.body="type")
    2: required string content (api.body="content")
    3: optional i32 status (api.body="status")
    4: optional i64 start_at (api.body="start_at")
    5: optional i64 end_at (api.body="end_at")
    6: optional string audience_expression (api.body="audience_expression")
    7: optional string audience_type (api.body="audience_type")
}

struct UpdateSystemNotificationReq {
    1: required i64 notification_id (api.path="notification_id")
    2: optional string type (api.body="type")
    3: optional string content (api.body="content")
    4: optional i32 status (api.body="status")
    5: optional i64 start_at (api.body="start_at")
    6: optional i64 end_at (api.body="end_at")
    7: optional string audience_expression (api.body="audience_expression")
    8: optional string audience_type (api.body="audience_type")
}

struct OfflineSystemNotificationReq {
    1: required i64 notification_id (api.path="notification_id")
}

struct SystemNotificationData {
    1: SystemNotificationInfo notification
}

struct SystemNotificationResp {
    1: i32 code
    2: string msg
    3: SystemNotificationData data
}

// ===== Service =====

service ConsoleService {
    ListAgentsResp ListAgents(1: ListAgentsReq req) (api.get="/console/api/v1/agents")
    UpdateAgentResp UpdateAgent(1: UpdateAgentReq req) (api.put="/console/api/v1/agents/:agent_id")
    GetAgentResp GetAgent(1: GetAgentReq req) (api.get="/console/api/v1/agents/:agent_id")
    ListItemsResp ListItems(1: ListItemsReq req) (api.get="/console/api/v1/items")
    UpdateItemResp UpdateItem(1: UpdateItemReq req) (api.put="/console/api/v1/items/:item_id")
    ListAgentImprItemsResp ListAgentImprItems(1: ListAgentImprItemsReq req) (api.get="/console/api/v1/impr/items")
    ListMilestoneRulesResp ListMilestoneRules(1: ListMilestoneRulesReq req) (api.get="/console/api/v1/milestone-rules")
    MilestoneRuleResp CreateMilestoneRule(1: CreateMilestoneRuleReq req) (api.post="/console/api/v1/milestone-rules")
    MilestoneRuleResp UpdateMilestoneRule(1: UpdateMilestoneRuleReq req) (api.put="/console/api/v1/milestone-rules/:rule_id")
    ReplaceMilestoneRuleResp ReplaceMilestoneRule(1: ReplaceMilestoneRuleReq req) (api.post="/console/api/v1/milestone-rules/:rule_id/replace")
    ListSystemNotificationsResp ListSystemNotifications(1: ListSystemNotificationsReq req) (api.get="/console/api/v1/system-notifications")
    SystemNotificationResp CreateSystemNotification(1: CreateSystemNotificationReq req) (api.post="/console/api/v1/system-notifications")
    SystemNotificationResp UpdateSystemNotification(1: UpdateSystemNotificationReq req) (api.put="/console/api/v1/system-notifications/:notification_id")
    SystemNotificationResp OfflineSystemNotification(1: OfflineSystemNotificationReq req) (api.post="/console/api/v1/system-notifications/:notification_id/offline")
}
