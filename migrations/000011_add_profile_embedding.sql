-- +goose Up
-- +goose StatementBegin
ALTER TABLE agent_profiles
  ADD COLUMN profile_embedding BYTEA DEFAULT NULL,
  ADD COLUMN embedding_model VARCHAR(100) DEFAULT '';

CREATE TABLE feedback_logs (
  id BIGSERIAL PRIMARY KEY,
  stream_message_id VARCHAR(64) NOT NULL,
  impression_id VARCHAR(64) NOT NULL DEFAULT '',
  agent_id BIGINT NOT NULL,
  item_id BIGINT NOT NULL,
  score SMALLINT NOT NULL,
  feedback_at BIGINT NOT NULL,
  created_at BIGINT NOT NULL
);

CREATE UNIQUE INDEX uq_feedback_logs_stream_message_id ON feedback_logs (stream_message_id);
CREATE INDEX idx_feedback_logs_agent_feedback_at ON feedback_logs (agent_id, feedback_at);
CREATE INDEX idx_feedback_logs_item_feedback_at ON feedback_logs (item_id, feedback_at);
CREATE INDEX idx_feedback_logs_impression_id ON feedback_logs (impression_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_feedback_logs_impression_id;
DROP INDEX IF EXISTS idx_feedback_logs_item_feedback_at;
DROP INDEX IF EXISTS idx_feedback_logs_agent_feedback_at;
DROP INDEX IF EXISTS uq_feedback_logs_stream_message_id;
DROP TABLE IF EXISTS feedback_logs;

ALTER TABLE agent_profiles
  DROP COLUMN IF EXISTS profile_embedding,
  DROP COLUMN IF EXISTS embedding_model;
-- +goose StatementEnd
