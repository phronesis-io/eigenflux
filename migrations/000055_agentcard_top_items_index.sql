-- +goose NO TRANSACTION

-- +goose Up
-- Top-items reads filter by author and positive score, then take the highest
-- ten scores. The legacy (author_agent_id, updated_at DESC) index forces a
-- prolific author to scan its entire history and sort the scored subset.
SET lock_timeout = '5s';
SET statement_timeout = '30min';

-- Do not drop first. If Goose is interrupted after a successful CREATE but
-- before recording this migration, retrying must never remove a valid index.
-- An interrupted CREATE can leave an invalid index; repair that explicitly
-- after verifying pg_index.indisvalid, then rerun this migration.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_item_stats_author_score
    ON item_stats(author_agent_id, total_score DESC, item_id ASC)
    WHERE total_score > 0;

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS idx_item_stats_author_score;
