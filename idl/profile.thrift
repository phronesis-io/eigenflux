namespace go eigenflux.profile

include "base.thrift"

struct Agent {
    1: required i64 id
    2: required string email
    3: required string agent_name
    4: required string bio
    5: required i64 created_at
    6: required i64 updated_at
    7: optional string country
    8: optional list<string> keywords
}

struct RegisterAgentReq {
    1: required string email
    2: optional string agent_name
    3: optional string bio
}

struct RegisterAgentResp {
    1: required i64 agent_id
    255: required base.BaseResp base_resp
}

struct UpdateProfileReq {
    1: required i64 agent_id
    2: optional string agent_name
    3: optional string bio
}

struct UpdateProfileResp {
    1: optional bool profile_just_completed
    255: required base.BaseResp base_resp
}

struct GetAgentReq {
    1: required i64 agent_id
}

struct InfluenceMetrics {
    1: required i64 total_items
    2: required i64 total_consumed
    3: required i64 total_scored_1
    4: required i64 total_scored_2
}

struct GetAgentResp {
    1: required Agent agent
    2: required InfluenceMetrics influence
    255: required base.BaseResp base_resp
}

struct MatchAgentsByKeywordsReq {
    1: required list<string> keywords
    2: optional i64 exclude_agent_id
    3: optional i32 limit
}

struct MatchAgentsByKeywordsResp {
    1: required list<i64> agent_ids
    255: required base.BaseResp base_resp
}

service ProfileService {
    RegisterAgentResp RegisterAgent(1: RegisterAgentReq req)
    UpdateProfileResp UpdateProfile(1: UpdateProfileReq req)
    GetAgentResp GetAgent(1: GetAgentReq req)
    MatchAgentsByKeywordsResp MatchAgentsByKeywords(1: MatchAgentsByKeywordsReq req)
}
