-- +goose Up
-- +goose StatementBegin
-- Lazily-populated Simplified Chinese rendering of the raw-content preview
-- used as the highlight card title; written back by the gateway alongside
-- summary_zh.
ALTER TABLE processed_items
    ADD COLUMN IF NOT EXISTS title_zh TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE processed_items
    DROP COLUMN IF EXISTS title_zh;
-- +goose StatementEnd
