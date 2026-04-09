-- +goose Up
-- +goose StatementBegin
ALTER TABLE agent_profiles
  ADD COLUMN profile_embedding BYTEA DEFAULT NULL,
  ADD COLUMN embedding_model VARCHAR(100) DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_profiles
  DROP COLUMN IF EXISTS profile_embedding,
  DROP COLUMN IF EXISTS embedding_model;
-- +goose StatementEnd
