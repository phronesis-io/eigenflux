-- +goose Up
ALTER TABLE integration_agents ADD COLUMN request_count BIGINT NOT NULL DEFAULT 0;
ALTER TABLE integration_agents ADD COLUMN last_request_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE integration_agents ADD COLUMN last_failure_code TEXT NOT NULL DEFAULT '';
ALTER TABLE integration_agents ADD COLUMN last_failure_at BIGINT NOT NULL DEFAULT 0;

ALTER TABLE integration_installations ADD COLUMN sdk_state TEXT NOT NULL DEFAULT '';
ALTER TABLE integration_installations ADD COLUMN sdk_failure_code TEXT NOT NULL DEFAULT '';
ALTER TABLE integration_installations ADD COLUMN sdk_state_reported_at BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE integration_installations DROP COLUMN sdk_state_reported_at;
ALTER TABLE integration_installations DROP COLUMN sdk_failure_code;
ALTER TABLE integration_installations DROP COLUMN sdk_state;
ALTER TABLE integration_agents DROP COLUMN last_failure_at;
ALTER TABLE integration_agents DROP COLUMN last_failure_code;
ALTER TABLE integration_agents DROP COLUMN last_request_at;
ALTER TABLE integration_agents DROP COLUMN request_count;
