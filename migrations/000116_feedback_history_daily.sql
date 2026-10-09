-- +goose Up
SET LOCAL lock_timeout = '5s';
-- Additive schema only. Backfill runs separately in bounded daily transactions.
CREATE TABLE feedback_history_daily (
    day DATE PRIMARY KEY,
    feedback_events BIGINT NOT NULL CHECK (feedback_events>=0),
    feedback_agents BIGINT NOT NULL CHECK (feedback_agents>=0),
    score_neg1 BIGINT NOT NULL,
    score_0 BIGINT NOT NULL,
    score_1 BIGINT NOT NULL,
    score_2 BIGINT NOT NULL,
    top3_feedback_events BIGINT NOT NULL,
    pgc_feedback_events BIGINT NOT NULL,
    ugc_feedback_events BIGINT NOT NULL,
    delivery_rows BIGINT,
    delivery_agents BIGINT,
    participating_agents BIGINT,
    snapshot_at TIMESTAMPTZ NOT NULL,
    CHECK (feedback_events=score_neg1+score_0+score_1+score_2),
    CHECK (top3_feedback_events<=feedback_events)
);
REVOKE ALL ON feedback_history_daily FROM PUBLIC;
CREATE VIEW grafana_feedback_history_daily WITH (security_barrier=true) AS
SELECT * FROM feedback_history_daily;
REVOKE ALL ON grafana_feedback_history_daily FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='grafana_ro_v2') THEN
  GRANT SELECT ON grafana_feedback_history_daily TO grafana_ro_v2;
 END IF;
END $$;
-- +goose StatementEnd
COMMENT ON VIEW grafana_feedback_history_daily IS
 'All valid score events, including legacy/unattributed/internal feedback. Shanghai days; not strict recommendation attribution. Delivery counts are NULL before the first complete available replay day. Distinct agents are daily and must not be summed.';
-- +goose Down
DROP VIEW grafana_feedback_history_daily;
DROP TABLE feedback_history_daily;
