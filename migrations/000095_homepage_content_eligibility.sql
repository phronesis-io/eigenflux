-- +goose Up
-- +goose StatementBegin
ALTER TABLE processed_items
    ADD COLUMN IF NOT EXISTS homepage_eligible BOOLEAN,
    ADD COLUMN IF NOT EXISTS homepage_rejection_reason VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS homepage_evaluation_version VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS homepage_evaluated_at BIGINT,
    ADD COLUMN IF NOT EXISTS homepage_evaluation_retry_at BIGINT;

ALTER TABLE processed_items
    ADD CONSTRAINT chk_processed_items_homepage_rejection_reason
    CHECK (homepage_rejection_reason IN (
        '', 'internal_log', 'advertising', 'politics', 'sexual',
        'ai_native_autonomy', 'low_substance', 'other'
    )) NOT VALID;

CREATE INDEX IF NOT EXISTS idx_processed_items_homepage_eligible_updated
    ON processed_items (updated_at DESC, item_id DESC)
    WHERE status = 3 AND homepage_eligible = TRUE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_processed_items_homepage_eligible_updated;
ALTER TABLE processed_items
    DROP CONSTRAINT IF EXISTS chk_processed_items_homepage_rejection_reason,
    DROP COLUMN IF EXISTS homepage_evaluation_retry_at,
    DROP COLUMN IF EXISTS homepage_evaluated_at,
    DROP COLUMN IF EXISTS homepage_evaluation_version,
    DROP COLUMN IF EXISTS homepage_rejection_reason,
    DROP COLUMN IF EXISTS homepage_eligible;
-- +goose StatementEnd
