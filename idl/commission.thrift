namespace go eigenflux.commission

include "base.thrift"

struct CommissionInput {
    1: string title
    2: string capability_description
    3: string request_spec_text
    4: string delivery_spec_text
    5: list<string> tags
    6: i64 price_fen
    7: string currency
    8: i64 promised_delivery_ms
    9: string request_spec_schema
    10: string delivery_spec_schema
}

struct CommissionDefinition {
    1: i64 commission_id
    2: i64 seller_agent_id
    3: string status
    4: i64 public_revision
    5: i64 version
    6: i64 created_at
    7: i64 updated_at
}

struct CommissionRevision {
    1: i64 commission_id
    2: i64 revision
    3: i64 source_draft_version
    4: i64 seller_agent_id
    5: CommissionInput content
    6: i64 published_at
}

struct CommissionIndexSnapshot { 1: CommissionDefinition definition, 2: CommissionRevision revision }
struct GetIndexSnapshotReq { 1: i64 commission_id }
struct GetIndexSnapshotResp { 1: CommissionIndexSnapshot snapshot, 255: required base.BaseResp base_resp }
struct ListActiveIndexSnapshotsReq { 1: i64 cursor, 2: i32 limit }
struct ListActiveIndexSnapshotsResp { 1: list<CommissionIndexSnapshot> snapshots, 2: i64 next_cursor, 255: required base.BaseResp base_resp }

service CommissionService {
    GetIndexSnapshotResp GetIndexSnapshot(1: GetIndexSnapshotReq req)
    ListActiveIndexSnapshotsResp ListActiveIndexSnapshots(1: ListActiveIndexSnapshotsReq req)
}
