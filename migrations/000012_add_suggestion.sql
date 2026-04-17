-- +goose Up
-- +goose StatementBegin
ALTER TABLE processed_items ADD COLUMN suggestion TEXT DEFAULT NULL;
CREATE INDEX idx_processed_items_suggestion_null ON processed_items (item_id) WHERE suggestion IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_processed_items_suggestion_null;
ALTER TABLE processed_items DROP COLUMN IF EXISTS suggestion;
-- +goose StatementEnd
