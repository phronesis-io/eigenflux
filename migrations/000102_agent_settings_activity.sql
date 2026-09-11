-- +goose Up
ALTER TABLE agent_settings ADD COLUMN last_activity_at BIGINT NOT NULL DEFAULT 0;
COMMENT ON COLUMN agent_settings.last_activity_at IS 'Latest successful authenticated Agent activity (epoch milliseconds); excludes Console browsing and server-generated events. Zero means unobserved.';

-- +goose Down
ALTER TABLE agent_settings DROP COLUMN last_activity_at;
