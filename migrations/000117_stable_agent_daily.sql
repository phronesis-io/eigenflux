-- +goose Up
SET LOCAL lock_timeout = '5s';
-- Additive schema only. Rows are filled by pipeline-cron in bounded daily transactions.
CREATE TABLE stable_agent_daily (
    day DATE PRIMARY KEY,
    stable_agents BIGINT NOT NULL CHECK (stable_agents>=0),
    scoring_agents BIGINT NOT NULL CHECK (scoring_agents>=0 AND scoring_agents<=stable_agents),
    feedback_events BIGINT NOT NULL CHECK (feedback_events>=0),
    positive_events BIGINT NOT NULL CHECK (positive_events>=0 AND positive_events<=feedback_events),
    pgc_positive_events BIGINT NOT NULL CHECK (pgc_positive_events>=0),
    ugc_positive_events BIGINT NOT NULL CHECK (ugc_positive_events>=0),
    pgc_feedback_events BIGINT NOT NULL CHECK (pgc_feedback_events>=0),
    ugc_feedback_events BIGINT NOT NULL CHECK (ugc_feedback_events>=0),
    snapshot_at TIMESTAMPTZ NOT NULL,
    CHECK (pgc_positive_events+ugc_positive_events<=positive_events),
    CHECK (pgc_feedback_events+ugc_feedback_events<=feedback_events)
);
REVOKE ALL ON stable_agent_daily FROM PUBLIC;
CREATE VIEW grafana_stable_agent_daily WITH (security_barrier=true) AS
SELECT * FROM stable_agent_daily;
REVOKE ALL ON grafana_stable_agent_daily FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='grafana_ro_v2') THEN
  GRANT SELECT ON grafana_stable_agent_daily TO grafana_ro_v2;
 END IF;
END $$;
-- +goose StatementEnd
COMMENT ON VIEW grafana_stable_agent_daily IS
 'Per Shanghai day D: external Agents with valid scores on at least 5 of the 7 days before D, and their valid score events on D. Counts only; no Agent identifiers.';
-- +goose Down
DROP VIEW grafana_stable_agent_daily;
DROP TABLE stable_agent_daily;
