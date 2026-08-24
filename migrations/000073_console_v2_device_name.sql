-- +goose Up
-- Computer name reported by the authenticated CLI that provisions an Agent or
-- creates a Console V2 handoff. It is presentation metadata only.
SET LOCAL lock_timeout = '5s';

ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS device_name VARCHAR(128) NOT NULL DEFAULT '';

-- +goose Down
SET LOCAL lock_timeout = '5s';

ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS device_name;
