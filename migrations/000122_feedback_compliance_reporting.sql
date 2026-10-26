-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE feedback_compliance_daily
  ADD COLUMN feedback_events_keyed BIGINT NOT NULL DEFAULT 0 CHECK (feedback_events_keyed >= 0),
  ADD COLUMN feedback_events_cli_inferred BIGINT NOT NULL DEFAULT 0 CHECK (feedback_events_cli_inferred >= 0),
  ADD COLUMN surface_declared_known_exposures BIGINT NOT NULL DEFAULT 0 CHECK (surface_declared_known_exposures >= 0),
  ADD COLUMN surface_declared_exposures BIGINT NOT NULL DEFAULT 0 CHECK (surface_declared_exposures >= 0),
  ADD COLUMN surface_agreement_exposures BIGINT NOT NULL DEFAULT 0 CHECK (surface_agreement_exposures >= 0),
  ADD COLUMN health_reported_agents BIGINT NOT NULL DEFAULT 0 CHECK (health_reported_agents >= 0),
  ADD COLUMN silent_health_reported_agents BIGINT NOT NULL DEFAULT 0 CHECK (silent_health_reported_agents >= 0),
  ADD COLUMN silent_attempting_agents BIGINT NOT NULL DEFAULT 0 CHECK (silent_attempting_agents >= 0),
  ADD COLUMN health_feedback_attempts BIGINT NOT NULL DEFAULT 0 CHECK (health_feedback_attempts >= 0),
  ADD COLUMN health_feedback_failures BIGINT NOT NULL DEFAULT 0 CHECK (health_feedback_failures >= 0),
  ADD COLUMN health_queue_drops BIGINT NOT NULL DEFAULT 0 CHECK (health_queue_drops >= 0),
  ADD CONSTRAINT chk_compliance_keyed CHECK (feedback_events_keyed <= feedback_events),
  ADD CONSTRAINT chk_compliance_cli_inferred CHECK (feedback_events_cli_inferred <= feedback_events_exact),
  ADD CONSTRAINT chk_compliance_declared CHECK (surface_declared_exposures <= surface_declared_known_exposures),
  ADD CONSTRAINT chk_compliance_agreement CHECK (surface_agreement_exposures <= surface_declared_known_exposures),
  ADD CONSTRAINT chk_compliance_declared_known CHECK (surface_declared_known_exposures <= scored_48h_exposures),
  ADD CONSTRAINT chk_compliance_health_reported CHECK (health_reported_agents <= delivered_agents),
  ADD CONSTRAINT chk_compliance_silent_attempting CHECK (silent_attempting_agents <= silent_health_reported_agents),
  ADD CONSTRAINT chk_compliance_silent_health CHECK (silent_health_reported_agents <= silent_agents),
  ADD CONSTRAINT chk_compliance_health_failures CHECK (health_feedback_failures <= health_feedback_attempts),
  DROP CONSTRAINT feedback_compliance_daily_dimension_check,
  ADD CONSTRAINT feedback_compliance_daily_dimension_check CHECK (dimension IN ('all','runtime_name','runtime_mode','cli_version','model','skill_revision','heartbeat_contract_version','auto_comment','feedback_contract_version'));
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
 'Anonymous external-consumer counts by Shanghai delivery day and event day. Exposure coverage excludes search and requires a unique impression/Agent/item key and 48h maturity; event attribution includes search. Cohorts use refresh-time settings, including backfills, with values below 5 delivered Agents folded into other. Final at day+3 Shanghai midnight. No Agent, item or impression identifiers or content. Inferred counts are separate from exact and use a unique delivered exposure in the preceding 48 h. Client declarations and health reports remain distinct from server-observed surface and score facts; contract cohorts use the latest score in the observation window.';
-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP VIEW grafana_feedback_compliance_daily;
DELETE FROM feedback_compliance_daily WHERE dimension='feedback_contract_version';
ALTER TABLE feedback_compliance_daily
  DROP CONSTRAINT chk_compliance_keyed,
  DROP CONSTRAINT chk_compliance_cli_inferred,
  DROP CONSTRAINT chk_compliance_declared,
  DROP CONSTRAINT chk_compliance_agreement,
  DROP CONSTRAINT chk_compliance_declared_known,
  DROP CONSTRAINT chk_compliance_health_reported,
  DROP CONSTRAINT chk_compliance_silent_attempting,
  DROP CONSTRAINT chk_compliance_silent_health,
  DROP CONSTRAINT chk_compliance_health_failures,
  DROP COLUMN feedback_events_keyed,
  DROP COLUMN feedback_events_cli_inferred,
  DROP COLUMN surface_declared_known_exposures,
  DROP COLUMN surface_declared_exposures,
  DROP COLUMN surface_agreement_exposures,
  DROP COLUMN health_reported_agents,
  DROP COLUMN silent_health_reported_agents,
  DROP COLUMN silent_attempting_agents,
  DROP COLUMN health_feedback_attempts,
  DROP COLUMN health_feedback_failures,
  DROP COLUMN health_queue_drops,
  DROP CONSTRAINT feedback_compliance_daily_dimension_check,
  ADD CONSTRAINT feedback_compliance_daily_dimension_check CHECK (dimension IN ('all','runtime_name','runtime_mode','cli_version','model','skill_revision','heartbeat_contract_version','auto_comment'));
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
