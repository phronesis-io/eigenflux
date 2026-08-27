-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS heartbeat_contract_version VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS skill_revision VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS heartbeat_reported_at BIGINT NOT NULL DEFAULT 0;

-- +goose Down
SET LOCAL lock_timeout = '5s';

ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS heartbeat_reported_at,
    DROP COLUMN IF EXISTS skill_revision,
    DROP COLUMN IF EXISTS heartbeat_contract_version;
