-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE feedback_compliance_daily
  ADD COLUMN scored_48h_inferred_exposures BIGINT NOT NULL DEFAULT 0 CHECK (scored_48h_inferred_exposures >= 0),
  ADD COLUMN unscored_batches_with_inferred BIGINT NOT NULL DEFAULT 0 CHECK (unscored_batches_with_inferred >= 0),
  ADD COLUMN feedback_events_inferred BIGINT NOT NULL DEFAULT 0 CHECK (feedback_events_inferred >= 0),
  ADD COLUMN silent_agents BIGINT NOT NULL DEFAULT 0 CHECK (silent_agents >= 0),
  ADD CONSTRAINT chk_compliance_inferred_coverage CHECK (scored_48h_exposures + scored_48h_inferred_exposures <= mature_exposures),
  ADD CONSTRAINT chk_compliance_inferred_batches CHECK (unscored_batches_with_inferred <= unscored_batches),
  ADD CONSTRAINT chk_compliance_inferred_events CHECK (feedback_events_with_impression + feedback_events_inferred <= feedback_events),
  ADD CONSTRAINT chk_compliance_silent CHECK (silent_agents <= delivered_agents);
DROP VIEW grafana_feedback_compliance_daily;
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
 'Anonymous external-consumer counts by Shanghai delivery day and event day. Exposure coverage excludes search and requires a unique impression/Agent/item key and 48h maturity; event attribution includes search. Cohorts use refresh-time settings, including backfills, with values below 5 delivered Agents folded into other. Final at day+3 Shanghai midnight. No Agent, item or impression identifiers or content. Inferred counts are separate from exact and use a unique delivered exposure in the preceding 48 h.';
-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP VIEW grafana_feedback_compliance_daily;
ALTER TABLE feedback_compliance_daily
  DROP CONSTRAINT chk_compliance_inferred_coverage,
  DROP CONSTRAINT chk_compliance_inferred_batches,
  DROP CONSTRAINT chk_compliance_inferred_events,
  DROP CONSTRAINT chk_compliance_silent,
  DROP COLUMN scored_48h_inferred_exposures,
  DROP COLUMN unscored_batches_with_inferred,
  DROP COLUMN feedback_events_inferred,
  DROP COLUMN silent_agents;
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
