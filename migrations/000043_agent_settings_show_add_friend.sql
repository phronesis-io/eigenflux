-- +goose Up
ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS show_add_friend BOOLEAN DEFAULT true;

-- +goose Down
ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS show_add_friend;
