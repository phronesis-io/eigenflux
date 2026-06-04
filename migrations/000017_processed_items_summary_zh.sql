-- +goose Up
-- +goose StatementBegin
-- Lazily-populated Simplified Chinese rendering of summary, written back by
-- the gateway the first time a zh-UI user views a non-Chinese item.
ALTER TABLE processed_items
    ADD COLUMN IF NOT EXISTS summary_zh TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE processed_items
    DROP COLUMN IF EXISTS summary_zh;
-- +goose StatementEnd
