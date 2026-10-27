-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE feedback_logs
  ADD COLUMN IF NOT EXISTS client_key VARCHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS surfaced BOOLEAN,
  ADD COLUMN IF NOT EXISTS impression_source VARCHAR(16) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS contract_version VARCHAR(32) NOT NULL DEFAULT '';

CREATE TABLE agent_feedback_health_daily (
  agent_id BIGINT NOT NULL,
  day DATE NOT NULL,
  reported_at BIGINT NOT NULL,
  window_ms BIGINT NOT NULL CHECK (window_ms BETWEEN 1 AND 172800000),
  pulls INTEGER NOT NULL CHECK (pulls BETWEEN 0 AND 1000000),
  delivered_items INTEGER NOT NULL CHECK (delivered_items BETWEEN 0 AND 1000000),
  scored_items INTEGER NOT NULL CHECK (scored_items BETWEEN 0 AND 1000000),
  status_runs INTEGER NOT NULL CHECK (status_runs BETWEEN 0 AND 1000000),
  feedback_attempts INTEGER NOT NULL CHECK (feedback_attempts BETWEEN 0 AND 1000000),
  feedback_failures INTEGER NOT NULL CHECK (feedback_failures BETWEEN 0 AND feedback_attempts),
  queue_drops INTEGER NOT NULL CHECK (queue_drops BETWEEN 0 AND 1000000),
  queue_depth INTEGER NOT NULL CHECK (queue_depth BETWEEN 0 AND 1000000),
  oldest_queued_age_ms BIGINT NOT NULL CHECK (oldest_queued_age_ms BETWEEN 0 AND 604800000),
  cli_version VARCHAR(32) NOT NULL DEFAULT '',
  PRIMARY KEY (agent_id, day)
);
REVOKE ALL ON agent_feedback_health_daily FROM PUBLIC;

-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP TABLE agent_feedback_health_daily;
ALTER TABLE feedback_logs
 DROP COLUMN contract_version, DROP COLUMN impression_source, DROP COLUMN surfaced, DROP COLUMN client_key;
