-- +goose Up
ALTER TABLE processed_items
    ADD COLUMN IF NOT EXISTS distribution_skip_reason VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS duplicate_of_item_id BIGINT NULL REFERENCES raw_items(item_id) ON DELETE SET NULL;

ALTER TABLE processed_items
    DROP CONSTRAINT IF EXISTS chk_processed_items_distribution_skip_reason;

ALTER TABLE processed_items
    ADD CONSTRAINT chk_processed_items_distribution_skip_reason
    CHECK (distribution_skip_reason IN ('', 'content_evaluation', 'duplicate'));

CREATE INDEX IF NOT EXISTS idx_processed_items_duplicate_of
    ON processed_items (duplicate_of_item_id)
    WHERE duplicate_of_item_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_processed_items_duplicate_of;

ALTER TABLE processed_items
    DROP CONSTRAINT IF EXISTS chk_processed_items_distribution_skip_reason,
    DROP COLUMN IF EXISTS duplicate_of_item_id,
    DROP COLUMN IF EXISTS distribution_skip_reason;
