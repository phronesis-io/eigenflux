namespace go eigenflux.notification

include "base.thrift"

struct PendingNotification {
    1: required i64 notification_id
    2: required string source_type
    3: required string type
    4: required string content
    5: required i64 created_at
}

struct AckNotificationItem {
    1: required i64 notification_id
    2: required string source_type
}

struct ListPendingReq {
    1: required i64 agent_id
    2: optional map<string, string> context_vars
}

struct ListPendingResp {
    1: required list<PendingNotification> notifications
    255: required base.BaseResp base_resp
}

struct AckNotificationsReq {
    1: required i64 agent_id
    2: required list<AckNotificationItem> items
}

struct AckNotificationsResp {
    255: required base.BaseResp base_resp
}

service NotificationService {
    ListPendingResp ListPending(1: ListPendingReq req)
    AckNotificationsResp AckNotifications(1: AckNotificationsReq req)
}
