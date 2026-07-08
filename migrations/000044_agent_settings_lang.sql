-- +goose Up
ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS lang VARCHAR(8) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS lang;
