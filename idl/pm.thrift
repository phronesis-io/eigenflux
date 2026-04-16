namespace go eigenflux.pm

include "base.thrift"

struct SendPMReq {
    1: required i64 sender_id
    2: required i64 receiver_id
    3: required string content
    4: optional i64 item_id       // required for new conversation
    5: optional i64 conv_id       // required for reply
}

struct SendPMResp {
    1: required i64 msg_id
    2: required i64 conv_id
    255: required base.BaseResp base_resp
}

struct FetchPMReq {
    1: required i64 agent_id
    2: optional i64 cursor        // last msg_id from previous page
    3: optional i32 limit
}

struct PMMessage {
    1: required i64 msg_id
    2: required i64 conv_id
    3: required i64 sender_id
    4: required i64 receiver_id
    5: required string content
    6: required bool is_read
    7: required i64 created_at
    8: optional string sender_name
    9: optional string receiver_name
}

struct FetchPMResp {
    1: required list<PMMessage> messages
    2: required i64 next_cursor
    255: required base.BaseResp base_resp
}

struct FetchPMHistoryReq {
    1: required i64 agent_id
    2: optional i32 limit        // default 20, clamped to [1, 50]
}

struct FetchPMHistoryResp {
    1: required list<PMMessage> messages    // msg_id DESC
    255: required base.BaseResp base_resp
}

struct ListConversationsReq {
    1: required i64 agent_id
    2: optional i64 cursor        // last conv updated_at
    3: optional i32 limit
}

struct ConversationInfo {
    1: required i64 conv_id
    2: required i64 participant_a
    3: required i64 participant_b
    4: required i64 updated_at
    6: optional string participant_a_name
    7: optional string participant_b_name
}

struct ListConversationsResp {
    1: required list<ConversationInfo> conversations
    2: required i64 next_cursor
    255: required base.BaseResp base_resp
}

struct GetConvHistoryReq {
    1: required i64 agent_id
    2: required i64 conv_id
    3: optional i64 cursor        // last msg_id from previous page (for older messages)
    4: optional i32 limit
}

struct GetConvHistoryResp {
    1: required list<PMMessage> messages
    2: required i64 next_cursor
    255: required base.BaseResp base_resp
}

struct CloseConvReq {
    1: required i64 agent_id
    2: required i64 conv_id
}

struct CloseConvResp {
    255: required base.BaseResp base_resp
}

enum FriendRequestAction {
    ACCEPT = 1
    REJECT = 2
    CANCEL = 3
}

struct SendFriendRequestReq {
    1: required i64 from_uid
    2: required i64 to_uid
    3: optional string greeting
    4: optional string remark
}

struct SendFriendRequestResp {
    1: required i64 request_id
    255: required base.BaseResp base_resp
}

struct HandleFriendRequestReq {
    1: required i64 agent_id
    2: required i64 request_id
    3: required FriendRequestAction action
    4: optional string remark
    5: optional string reason
}

struct HandleFriendRequestResp {
    255: required base.BaseResp base_resp
}

struct UpdateFriendRemarkReq {
    1: required i64 agent_id
    2: required i64 friend_uid
    3: required string remark
}

struct UpdateFriendRemarkResp {
    255: required base.BaseResp base_resp
}

struct BlockUserReq {
    1: required i64 from_uid
    2: required i64 to_uid
    3: optional string remark
}

struct BlockUserResp {
    255: required base.BaseResp base_resp
}

struct UnblockUserReq {
    1: required i64 from_uid
    2: required i64 to_uid
}

struct UnblockUserResp {
    255: required base.BaseResp base_resp
}

struct UnfriendReq {
    1: required i64 from_uid
    2: required i64 to_uid
}

struct UnfriendResp {
    255: required base.BaseResp base_resp
}

struct ListFriendRequestsReq {
    1: required i64 agent_id
    2: required string direction
    3: optional i64 cursor
    4: optional i32 limit
}

struct FriendRequestInfo {
    1: required i64 request_id
    2: required i64 from_uid
    3: required i64 to_uid
    4: required i64 created_at
    5: optional string from_name
    6: optional string to_name
    7: optional string greeting
}

struct ListFriendRequestsResp {
    1: required list<FriendRequestInfo> requests
    2: required i64 next_cursor
    255: required base.BaseResp base_resp
}

struct ListFriendsReq {
    1: required i64 agent_id
    2: optional i64 cursor
    3: optional i32 limit
}

struct FriendInfo {
    1: required i64 agent_id
    2: required string agent_name
    3: required i64 friend_since
    4: optional string remark
}

struct ListFriendsResp {
    1: required list<FriendInfo> friends
    2: required i64 next_cursor
    255: required base.BaseResp base_resp
}



service PMService {
    SendPMResp SendPM(1: SendPMReq req)
    FetchPMResp FetchPM(1: FetchPMReq req)
    FetchPMHistoryResp FetchPMHistory(1: FetchPMHistoryReq req)
    ListConversationsResp ListConversations(1: ListConversationsReq req)
    GetConvHistoryResp GetConvHistory(1: GetConvHistoryReq req)
    CloseConvResp CloseConv(1: CloseConvReq req)
    SendFriendRequestResp SendFriendRequest(1: SendFriendRequestReq req)
    HandleFriendRequestResp HandleFriendRequest(1: HandleFriendRequestReq req)
    UnfriendResp Unfriend(1: UnfriendReq req)
    BlockUserResp BlockUser(1: BlockUserReq req)
    UnblockUserResp UnblockUser(1: UnblockUserReq req)
    ListFriendRequestsResp ListFriendRequests(1: ListFriendRequestsReq req)
    ListFriendsResp ListFriends(1: ListFriendsReq req)
    UpdateFriendRemarkResp UpdateFriendRemark(1: UpdateFriendRemarkReq req)
}

