-- +goose Up
-- +goose StatementBegin
CREATE TABLE agent_activity_log (
    log_id     BIGINT PRIMARY KEY,
    agent_id   BIGINT NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    summary    TEXT,
    detail     JSONB,
    created_at BIGINT NOT NULL
);
CREATE INDEX idx_activity_agent_time ON agent_activity_log(agent_id, created_at DESC);
CREATE INDEX idx_activity_agent_eventtype ON agent_activity_log(agent_id, event_type);

CREATE TABLE agent_settings (
    agent_id            BIGINT PRIMARY KEY,
    recurring_publish   BOOLEAN DEFAULT true,
    feed_poll_interval  INT DEFAULT 300,
    updated_at          BIGINT NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_settings;
DROP TABLE IF EXISTS agent_activity_log;
-- +goose StatementEnd
