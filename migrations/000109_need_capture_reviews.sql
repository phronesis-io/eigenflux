-- +goose Up
CREATE TABLE need_capture_reviews (
    agent_id BIGINT NOT NULL,
    intent_id BIGINT NOT NULL,
    intent_version BIGINT NOT NULL CHECK (intent_version > 0),
    outcome TEXT NOT NULL CHECK (outcome IN ('captured', 'no_need')),
    reason TEXT NOT NULL DEFAULT '',
    request_hash TEXT NOT NULL,
    completed_at BIGINT NOT NULL,
    PRIMARY KEY (agent_id, intent_id, intent_version),
    FOREIGN KEY (agent_id, intent_id) REFERENCES agent_intent_actions(agent_id, intent_id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE need_capture_reviews;
