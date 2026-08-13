-- +goose Up
ALTER TABLE processed_items
    ADD COLUMN IF NOT EXISTS distribution_skip_reason VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS duplicate_of_item_id BIGINT NULL REFERENCES raw_items(item_id) ON DELETE SET NULL;

ALTER TABLE processed_items
    DROP CONSTRAINT IF EXISTS chk_processed_items_distribution_skip_reason;

ALTER TABLE processed_items
    ADD CONSTRAINT chk_processed_items_distribution_skip_reason
    CHECK (distribution_skip_reason IN ('', 'content_evaluation', 'duplicate'));

-- Recover same-author exact duplicates among historical discarded broadcasts.
-- Older rows did not persist a reason, so anything not provably duplicate is
-- classified into the safe, generic content-evaluation category.
WITH exact_duplicates AS (
    SELECT discarded.item_id,
           prior.item_id AS duplicate_of_item_id
      FROM processed_items AS discarded
      JOIN raw_items AS current_raw ON current_raw.item_id = discarded.item_id
      JOIN LATERAL (
          SELECT prior_raw.item_id
            FROM raw_items AS prior_raw
            JOIN processed_items AS prior_processed ON prior_processed.item_id = prior_raw.item_id
           WHERE prior_raw.author_agent_id = current_raw.author_agent_id
             AND prior_raw.raw_content = current_raw.raw_content
             AND prior_processed.status = 3
             AND (
                 prior_raw.created_at < current_raw.created_at
                 OR (prior_raw.created_at = current_raw.created_at AND prior_raw.item_id < current_raw.item_id)
             )
           ORDER BY prior_raw.created_at DESC, prior_raw.item_id DESC
           LIMIT 1
      ) AS prior ON TRUE
     WHERE discarded.status = 4
       AND discarded.distribution_skip_reason = ''
)
UPDATE processed_items AS discarded
   SET distribution_skip_reason = 'duplicate',
       duplicate_of_item_id = exact_duplicates.duplicate_of_item_id
  FROM exact_duplicates
 WHERE discarded.item_id = exact_duplicates.item_id;

UPDATE processed_items
   SET distribution_skip_reason = 'content_evaluation'
 WHERE status = 4
   AND distribution_skip_reason = '';

CREATE INDEX IF NOT EXISTS idx_processed_items_duplicate_of
    ON processed_items (duplicate_of_item_id)
    WHERE duplicate_of_item_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_processed_items_duplicate_of;

ALTER TABLE processed_items
    DROP CONSTRAINT IF EXISTS chk_processed_items_distribution_skip_reason,
    DROP COLUMN IF EXISTS duplicate_of_item_id,
    DROP COLUMN IF EXISTS distribution_skip_reason;
