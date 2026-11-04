namespace go eigenflux.servicereview

// Dynamic snapshot data is projected by the handler without narrowing listing JSON.
struct ListServiceReviewsReq {
    1: optional string status (api.query="status")
    2: optional string cursor (api.query="cursor")
    3: optional i32 limit (api.query="limit")
}
struct ResolveServiceReviewReq {
    1: required string id (api.path="id")
    2: required string decision (api.body="decision")
    3: required string operator (api.body="operator")
    4: required string reason (api.body="reason")
}
struct ServiceReviewResp {
    1: required i32 code
    2: required string msg
}

service ServiceReviewService {
    ServiceReviewResp ListServiceReviews(1: ListServiceReviewsReq req) (api.get="/console/api/v1/service-reviews")
    ServiceReviewResp ResolveServiceReview(1: ResolveServiceReviewReq req) (api.post="/console/api/v1/service-reviews/:id/resolve")
}
