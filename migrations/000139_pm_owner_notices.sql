-- +goose Up
CREATE TABLE pm_owner_notices (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    conv_id BIGINT NOT NULL REFERENCES conversations(conv_id) ON DELETE CASCADE,
    result_id TEXT NOT NULL,
    summary TEXT NOT NULL CHECK (char_length(summary) BETWEEN 1 AND 300),
    needs_action BOOLEAN NOT NULL,
    generated_at BIGINT NOT NULL CHECK (generated_at > 0),
    UNIQUE (agent_id, result_id)
);
CREATE INDEX idx_pm_owner_notices_latest
    ON pm_owner_notices (agent_id, conv_id, generated_at DESC, id DESC);

-- +goose Down
DROP TABLE pm_owner_notices;
