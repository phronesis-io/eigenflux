// Minimal wire-compatible projection of Commission's internal moderation RPCs.
// Run scripts/generate_review_rpc.sh to regenerate. Keep field IDs synchronized.
namespace go eigenflux.commission

struct ReviewBaseResp {
    1: required i32 code
    2: required string msg
    3: optional string error_code
}
struct ListModerationsReq {
    1: required string status
    2: required i64 cursor
    3: required i32 limit
}
struct ListModerationsResp {
    1: required string result_json
    255: required ReviewBaseResp base_resp
}
struct ResolveModerationReq {
    1: required i64 moderation_id
    2: required string decision
    3: required string reviewer
    4: required string reason
}
struct ResolveModerationResp {
    1: required string result_json
    255: required ReviewBaseResp base_resp
}
service CommissionService {
    ListModerationsResp ListModerations(1: ListModerationsReq req)
    ResolveModerationResp ResolveModeration(1: ResolveModerationReq req)
}
