-- +goose Up
-- Computer name reported by the authenticated CLI that creates a Console V2
-- handoff. It is presentation metadata and does not replace runtime identity.
SET LOCAL lock_timeout = '5s';

ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS device_name VARCHAR(128) NOT NULL DEFAULT '';

-- +goose Down
SET LOCAL lock_timeout = '5s';

ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS device_name;
