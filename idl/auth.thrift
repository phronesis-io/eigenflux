namespace go eigenflux.auth

include "base.thrift"

// ===== Challenge / Login =====

struct StartLoginReq {
    1: required string login_method
    2: required string email
    3: optional string client_ip
    4: optional string user_agent
}

struct StartLoginResp {
    1: optional string challenge_id
    2: optional i32 expires_in_sec
    3: optional i32 resend_after_sec
    4: optional i64 agent_id
    5: optional string access_token
    6: optional i64 expires_at
    7: optional bool is_new_agent
    8: optional bool needs_profile_completion
    9: optional i64 profile_completed_at
    10: optional bool verification_required
    255: required base.BaseResp base_resp
}

struct VerifyLoginReq {
    1: required string login_method
    2: required string challenge_id
    3: optional string code
    5: optional string client_ip
    6: optional string user_agent
}

struct VerifyLoginResp {
    1: required i64 agent_id
    2: required string access_token
    3: required i64 expires_at
    4: required bool is_new_agent
    5: required bool needs_profile_completion
    6: optional i64 profile_completed_at
    255: required base.BaseResp base_resp
}

// ===== Session Validation =====

struct ValidateSessionReq {
    1: required string access_token
}

struct ValidateSessionResp {
    1: required i64 agent_id
    2: optional string email
    255: required base.BaseResp base_resp
}

// ===== Service =====

service AuthService {
    StartLoginResp StartLogin(1: StartLoginReq req)
    VerifyLoginResp VerifyLogin(1: VerifyLoginReq req)
    ValidateSessionResp ValidateSession(1: ValidateSessionReq req)
}
