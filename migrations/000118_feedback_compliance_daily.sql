-- +goose Up
SET LOCAL lock_timeout = '5s';
-- Additive schema only; pipeline-cron fills bounded daily snapshots.
CREATE TABLE feedback_compliance_daily (
    day DATE NOT NULL,
    dimension TEXT NOT NULL CHECK (dimension IN ('all','runtime_name','runtime_mode','cli_version','model','skill_revision','heartbeat_contract_version','auto_comment')),
    value TEXT NOT NULL CHECK (length(value) BETWEEN 1 AND 64),
    delivered_exposures BIGINT NOT NULL CHECK (delivered_exposures >= 0),
    delivered_agents BIGINT NOT NULL CHECK (delivered_agents >= 0),
    identifiable_exposures BIGINT NOT NULL CHECK (identifiable_exposures >= 0),
    mature_exposures BIGINT NOT NULL CHECK (mature_exposures >= 0),
    scored_1h_exposures BIGINT NOT NULL CHECK (scored_1h_exposures >= 0),
    scored_24h_exposures BIGINT NOT NULL CHECK (scored_24h_exposures >= 0),
    scored_48h_exposures BIGINT NOT NULL CHECK (scored_48h_exposures >= 0),
    mature_batches BIGINT NOT NULL CHECK (mature_batches >= 0),
    fully_scored_batches BIGINT NOT NULL CHECK (fully_scored_batches >= 0),
    partially_scored_batches BIGINT NOT NULL CHECK (partially_scored_batches >= 0),
    unscored_batches BIGINT NOT NULL CHECK (unscored_batches >= 0),
    degenerate_candidate_batches BIGINT NOT NULL CHECK (degenerate_candidate_batches >= 0),
    degenerate_batches BIGINT NOT NULL CHECK (degenerate_batches >= 0),
    surfaced_exposures BIGINT NOT NULL CHECK (surfaced_exposures >= 0),
    surfaced_unscored_exposures BIGINT NOT NULL CHECK (surfaced_unscored_exposures >= 0),
    surfaced_nonpositive_exposures BIGINT NOT NULL CHECK (surfaced_nonpositive_exposures >= 0),
    positive_exposures BIGINT NOT NULL CHECK (positive_exposures >= 0),
    positive_surfaced_exposures BIGINT NOT NULL CHECK (positive_surfaced_exposures >= 0),
    positive_batches BIGINT NOT NULL CHECK (positive_batches >= 0),
    positive_batches_without_surface BIGINT NOT NULL CHECK (positive_batches_without_surface >= 0),
    followup_exposures BIGINT NOT NULL CHECK (followup_exposures >= 0),
    followup_agent_pm_exposures BIGINT NOT NULL CHECK (followup_agent_pm_exposures >= 0),
    agent_pm_exposures BIGINT NOT NULL CHECK (agent_pm_exposures >= 0),
    author_replied_exposures BIGINT NOT NULL CHECK (author_replied_exposures >= 0),
    feedback_events BIGINT NOT NULL CHECK (feedback_events >= 0),
    feedback_events_with_impression BIGINT NOT NULL CHECK (feedback_events_with_impression >= 0),
    feedback_events_exact BIGINT NOT NULL CHECK (feedback_events_exact >= 0),
    surface_events BIGINT NOT NULL CHECK (surface_events >= 0),
    surface_events_with_impression BIGINT NOT NULL CHECK (surface_events_with_impression >= 0),
    surface_events_exact BIGINT NOT NULL CHECK (surface_events_exact >= 0),
    snapshot_at TIMESTAMPTZ NOT NULL,
    schema_version INTEGER NOT NULL DEFAULT 1 CHECK (schema_version = 1),
    PRIMARY KEY (day, dimension, value),
    CHECK (scored_1h_exposures <= scored_24h_exposures AND scored_24h_exposures <= scored_48h_exposures
      AND scored_48h_exposures <= mature_exposures AND mature_exposures <= identifiable_exposures
      AND identifiable_exposures <= delivered_exposures),
    CHECK (fully_scored_batches + partially_scored_batches + unscored_batches = mature_batches),
    CHECK (degenerate_batches <= degenerate_candidate_batches AND degenerate_candidate_batches <= mature_batches),
    CHECK (surfaced_unscored_exposures + surfaced_nonpositive_exposures <= surfaced_exposures),
    CHECK (positive_surfaced_exposures <= positive_exposures AND positive_exposures <= scored_48h_exposures),
    CHECK (positive_batches_without_surface <= positive_batches AND positive_batches <= mature_batches),
    CHECK (followup_agent_pm_exposures <= followup_exposures),
    CHECK (author_replied_exposures <= agent_pm_exposures AND agent_pm_exposures <= mature_exposures),
    CHECK (feedback_events_exact <= feedback_events_with_impression AND feedback_events_with_impression <= feedback_events),
    CHECK (surface_events_exact <= surface_events_with_impression AND surface_events_with_impression <= surface_events)
);
REVOKE ALL ON feedback_compliance_daily FROM PUBLIC;
CREATE VIEW grafana_feedback_compliance_daily WITH (security_barrier=true) AS
SELECT * FROM feedback_compliance_daily;
REVOKE ALL ON grafana_feedback_compliance_daily FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='grafana_ro_v2') THEN
  GRANT SELECT ON grafana_feedback_compliance_daily TO grafana_ro_v2;
 END IF;
END $$;
-- +goose StatementEnd
COMMENT ON VIEW grafana_feedback_compliance_daily IS
 'Anonymous external-consumer counts by Shanghai delivery day and event day. Exposure coverage excludes search and requires a unique impression/Agent/item key and 48h maturity; event attribution includes search. Cohorts use refresh-time settings, including backfills, with values below 5 delivered Agents folded into other. Final at day+3 Shanghai midnight. No Agent, item or impression identifiers or content.';
-- +goose Down
DROP VIEW grafana_feedback_compliance_daily;
DROP TABLE feedback_compliance_daily;
