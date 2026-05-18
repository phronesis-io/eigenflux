-- +goose Up
-- +goose StatementBegin
ALTER TABLE replay_logs ADD COLUMN client_meta TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE replay_logs DROP COLUMN IF EXISTS client_meta;
-- +goose StatementEnd
