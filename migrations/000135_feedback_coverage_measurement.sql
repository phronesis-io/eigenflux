-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE feedback_logs ADD COLUMN accepted_at BIGINT NOT NULL DEFAULT 0 CHECK (accepted_at >= 0);
COMMENT ON COLUMN feedback_logs.accepted_at IS 'Gateway acceptance time in epoch ms; zero is unknown. feedback_at remains consumer insertion time.';
ALTER TABLE feedback_compliance_daily
 ADD COLUMN scoring_agents BIGINT NOT NULL DEFAULT 0 CHECK (scoring_agents >= 0 AND scoring_agents <= delivered_agents),
 ADD COLUMN delayed_ingestion_exposures BIGINT NOT NULL DEFAULT 0 CHECK (delayed_ingestion_exposures >= 0 AND delayed_ingestion_exposures <= mature_exposures),
 DROP CONSTRAINT feedback_compliance_daily_dimension_check,
 ADD CONSTRAINT feedback_compliance_daily_dimension_check CHECK (dimension IN ('all','runtime_name','runtime_mode','cli_version','model','skill_revision','heartbeat_contract_version','auto_comment','feedback_contract_version','basis'));
DROP VIEW grafana_feedback_compliance_daily;
CREATE VIEW grafana_feedback_compliance_daily WITH (security_barrier=true) AS SELECT * FROM feedback_compliance_daily;
REVOKE ALL ON grafana_feedback_compliance_daily FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='grafana_ro_v2') THEN
  GRANT SELECT ON grafana_feedback_compliance_daily TO grafana_ro_v2;
 END IF;
END $$;
-- +goose StatementEnd
COMMENT ON VIEW grafana_feedback_compliance_daily IS 'Anonymous coverage counts. Existing exact/inferred 48h series use consumer insertion time. Basis rows partition delivered exposures (exclude baseline for personalized coverage); event and health metrics are not defined for basis rows. CLI cohorts use delivery-time evidence, unknown if missing and mixed for multiple versions. Other settings cohorts remain refresh-time descriptors. Scoring Agents count exact 48h scoring Agent-days; delayed ingestion means accepted within 48h but stored after 48h, observed by snapshot cutoff. Acceptance evidence starts with the API/worker rollout; no historical reconstruction.';

-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP VIEW grafana_feedback_compliance_daily;
DELETE FROM feedback_compliance_daily WHERE dimension='basis';
ALTER TABLE feedback_compliance_daily
 DROP COLUMN scoring_agents, DROP COLUMN delayed_ingestion_exposures,
 DROP CONSTRAINT feedback_compliance_daily_dimension_check,
 ADD CONSTRAINT feedback_compliance_daily_dimension_check CHECK (dimension IN ('all','runtime_name','runtime_mode','cli_version','model','skill_revision','heartbeat_contract_version','auto_comment','feedback_contract_version'));
ALTER TABLE feedback_logs DROP COLUMN accepted_at;
CREATE VIEW grafana_feedback_compliance_daily WITH (security_barrier=true) AS SELECT * FROM feedback_compliance_daily;
REVOKE ALL ON grafana_feedback_compliance_daily FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='grafana_ro_v2') THEN
  GRANT SELECT ON grafana_feedback_compliance_daily TO grafana_ro_v2;
 END IF;
END $$;
-- +goose StatementEnd
