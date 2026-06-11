-- +goose Up
-- +goose StatementBegin
-- AgentRapport quiz (public marketing activity, /api/v1/agti/*).
-- One session = one agent+human round. agent_answers is locked on first write
-- (commit-reveal); result rows are immutable and back the shareable result page.
CREATE TABLE agti_sessions (
    session_id      VARCHAR(32) PRIMARY KEY,
    question_ids    JSONB NOT NULL,
    agent_answers   JSONB,
    agent_locked_at BIGINT NOT NULL DEFAULT 0,
    human_answers   JSONB,
    result_id       VARCHAR(32) NOT NULL DEFAULT '',
    client_ip       VARCHAR(64) NOT NULL DEFAULT '',
    created_at      BIGINT NOT NULL
);
CREATE INDEX idx_agti_sessions_created ON agti_sessions (created_at);

CREATE TABLE agti_results (
    result_id   VARCHAR(32) PRIMARY KEY,
    session_id  VARCHAR(32) NOT NULL,
    type_code   VARCHAR(16) NOT NULL,
    match_count INT NOT NULL,
    payload     JSONB NOT NULL,
    created_at  BIGINT NOT NULL
);
CREATE INDEX idx_agti_results_created ON agti_results (created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE agti_results;
DROP TABLE agti_sessions;
-- +goose StatementEnd
