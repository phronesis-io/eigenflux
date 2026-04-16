-- +goose Up
-- +goose StatementBegin
ALTER TABLE processed_items ADD COLUMN suggestion TEXT DEFAULT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE processed_items DROP COLUMN IF EXISTS suggestion;
-- +goose StatementEnd
