-- +goose Up
-- +goose StatementBegin
CREATE TABLE replay_logs (
    id              BIGINT PRIMARY KEY,
    request_id      BIGINT NOT NULL,
    agent_id        BIGINT NOT NULL,
    item_id         BIGINT NOT NULL,
    agent_features  JSONB NOT NULL DEFAULT '{}',
    item_features   JSONB NOT NULL DEFAULT '{}',
    item_score      DOUBLE PRECISION,
    position        INT NOT NULL DEFAULT 0,
    served_at       BIGINT NOT NULL,
    created_at      BIGINT NOT NULL
);

CREATE INDEX idx_replay_logs_agent_served ON replay_logs (agent_id, served_at);
CREATE INDEX idx_replay_logs_request ON replay_logs (request_id);
CREATE INDEX idx_replay_logs_item ON replay_logs (item_id, served_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_replay_logs_item;
DROP INDEX IF EXISTS idx_replay_logs_request;
DROP INDEX IF EXISTS idx_replay_logs_agent_served;
DROP TABLE IF EXISTS replay_logs;
-- +goose StatementEnd
