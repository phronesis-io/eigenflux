-- +goose Up
ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS runtime_reported_at BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS runtime_reported_at;
