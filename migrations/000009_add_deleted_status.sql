-- +goose Up
-- Add deleted status (5) for user-initiated deletion
-- This is distinct from discarded (4) which is for pipeline quality rejection
ALTER TABLE processed_items
  DROP CONSTRAINT chk_processed_items_status,
  ADD CONSTRAINT chk_processed_items_status CHECK (status IN (0,1,2,3,4,5));

COMMENT ON COLUMN processed_items.status IS 'Processing status: 0=pending, 1=processing, 2=failed, 3=completed, 4=discarded, 5=deleted';

DROP INDEX IF EXISTS idx_rel_from;
CREATE INDEX idx_rel_from ON user_relations(from_uid, rel_type, id DESC);

DROP INDEX IF EXISTS idx_req_pending_to;
CREATE INDEX idx_req_pending_to ON friend_requests(to_uid, id DESC) WHERE status = 0;

DROP INDEX IF EXISTS idx_req_pending_from;
CREATE INDEX idx_req_pending_from ON friend_requests(from_uid, id DESC) WHERE status = 0;

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

DROP INDEX IF EXISTS idx_req_pending_from;
CREATE INDEX idx_req_pending_from ON friend_requests(from_uid, created_at DESC) WHERE status = 0;

DROP INDEX IF EXISTS idx_req_pending_to;
CREATE INDEX idx_req_pending_to ON friend_requests(to_uid, created_at DESC) WHERE status = 0;

DROP INDEX IF EXISTS idx_rel_from;
CREATE INDEX idx_rel_from ON user_relations(from_uid, rel_type);

ALTER TABLE processed_items
  DROP CONSTRAINT chk_processed_items_status,
  ADD CONSTRAINT chk_processed_items_status CHECK (status IN (0,1,2,3,4));

COMMENT ON COLUMN processed_items.status IS 'Processing status: 0=pending, 1=processing, 2=failed, 3=completed, 4=discarded';
