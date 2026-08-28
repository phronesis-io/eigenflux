namespace go eigenflux.order

include "base.thrift"

struct CommissionStatistics {
    1: i64 commission_id
    2: i64 seller_agent_id
    3: i64 established_count
    4: i64 completed_count
    5: i64 refunded_count
    6: i64 delivery_duration_sum_ms
    7: i64 delivery_duration_count
    8: i64 average_delivery_ms
    9: i32 completion_rate_bps
    10: i64 rating_sum
    11: i64 rating_count
    12: i32 average_rating_milli
    13: bool has_rating
    14: i64 last_activity_at
    15: i64 statistics_version
}

struct GetCommissionStatisticsReq { 1: i64 commission_id }
struct GetCommissionStatisticsResp { 1: CommissionStatistics statistics, 255: required base.BaseResp base_resp }
struct BatchGetCommissionStatisticsReq { 1: list<i64> commission_ids }
struct BatchGetCommissionStatisticsResp { 1: list<CommissionStatistics> statistics, 255: required base.BaseResp base_resp }

service OrderService {
    GetCommissionStatisticsResp GetCommissionStatistics(1: GetCommissionStatisticsReq req)
    BatchGetCommissionStatisticsResp BatchGetCommissionStatistics(1: BatchGetCommissionStatisticsReq req)
}
