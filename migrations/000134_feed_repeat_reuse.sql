-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_feedback_health_daily ADD COLUMN repeat_pulls_suppressed BIGINT NOT NULL DEFAULT 0 CHECK (repeat_pulls_suppressed BETWEEN 0 AND 1000000);

-- +goose Down
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_feedback_health_daily DROP COLUMN repeat_pulls_suppressed;
