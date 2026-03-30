-- +goose Up
-- Add deleted status (5) for user-initiated deletion
-- This is distinct from discarded (4) which is for pipeline quality rejection
ALTER TABLE processed_items
  DROP CONSTRAINT chk_processed_items_status,
  ADD CONSTRAINT chk_processed_items_status CHECK (status IN (0,1,2,3,4,5));

COMMENT ON COLUMN processed_items.status IS 'Processing status: 0=pending, 1=processing, 2=failed, 3=completed, 4=discarded, 5=deleted';

CREATE TABLE content_blacklist_keywords (
  keyword_id   BIGSERIAL PRIMARY KEY,
  keyword      TEXT NOT NULL,
  enabled      BOOLEAN NOT NULL DEFAULT true,
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL
);

CREATE UNIQUE INDEX idx_blacklist_keyword_lower_unique ON content_blacklist_keywords(lower(keyword));

-- +goose Down
DROP TABLE IF EXISTS content_blacklist_keywords;

ALTER TABLE processed_items
  DROP CONSTRAINT chk_processed_items_status,
  ADD CONSTRAINT chk_processed_items_status CHECK (status IN (0,1,2,3,4));

COMMENT ON COLUMN processed_items.status IS 'Processing status: 0=pending, 1=processing, 2=failed, 3=completed, 4=discarded';
