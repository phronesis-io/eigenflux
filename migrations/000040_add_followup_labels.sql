-- +goose Up
-- +goose StatementBegin
-- Implicit graded relevance labels reported by the eigenflux__followup plugin tool.
-- agent_id is the consuming agent (from the request bearer token); item_id is the
-- server item the agent surfaced/engaged/acted on. Joined offline with replay_logs
-- on (agent_id, item_id) with served_at <= reported_at, or exactly on impression_id
-- when present. dedup_key is the plugin-computed idempotency token.
CREATE TABLE followup_labels (
    id              BIGINT PRIMARY KEY,
    agent_id        BIGINT NOT NULL,
    item_id         BIGINT NOT NULL,
    kind            VARCHAR(16) NOT NULL,
    impression_id   VARCHAR(64) NOT NULL DEFAULT '',
    brief           TEXT NOT NULL DEFAULT '',
    session_key     VARCHAR(128) NOT NULL DEFAULT '',
    channel         VARCHAR(64) NOT NULL DEFAULT '',
    server_id       VARCHAR(64) NOT NULL DEFAULT '',
    dedup_key       VARCHAR(32) NOT NULL,
    reported_at     BIGINT NOT NULL,
    created_at      BIGINT NOT NULL
);

CREATE UNIQUE INDEX uq_followup_labels_dedup ON followup_labels (dedup_key);
CREATE INDEX idx_followup_labels_agent_item ON followup_labels (agent_id, item_id, reported_at);
CREATE INDEX idx_followup_labels_impression ON followup_labels (impression_id) WHERE impression_id <> '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_followup_labels_impression;
DROP INDEX IF EXISTS idx_followup_labels_agent_item;
DROP INDEX IF EXISTS uq_followup_labels_dedup;
DROP TABLE IF EXISTS followup_labels;
-- +goose StatementEnd
