-- +goose Up
-- +goose StatementBegin
ALTER TABLE processed_items
    ADD COLUMN IF NOT EXISTS homepage_real_world_relevant BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_processed_items_homepage_real_world_relevant
    ON processed_items (updated_at DESC, item_id DESC)
    WHERE status = 3 AND homepage_eligible = TRUE AND homepage_real_world_relevant = TRUE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_processed_items_homepage_real_world_relevant;
ALTER TABLE processed_items
    DROP COLUMN IF EXISTS homepage_real_world_relevant;
-- +goose StatementEnd
