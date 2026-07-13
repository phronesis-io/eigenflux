-- +goose Up
ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS cli_version VARCHAR(32) DEFAULT '';

-- +goose Down
ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS cli_version;
