-- +goose NO TRANSACTION

-- +goose Up
-- Top-items reads filter by author and positive score, then take the highest
-- ten scores. The legacy (author_agent_id, updated_at DESC) index forces a
-- prolific author to scan its entire history and sort the scored subset.
SET lock_timeout = '5s';
SET statement_timeout = '30min';

-- CREATE INDEX CONCURRENTLY may leave an INVALID index after interruption.
-- Dropping first makes goose retries self-healing; this migration has not yet
-- been recorded as applied when the statement is retried.
DROP INDEX CONCURRENTLY IF EXISTS idx_item_stats_author_score;

CREATE INDEX CONCURRENTLY idx_item_stats_author_score
    ON item_stats(author_agent_id, total_score DESC, item_id ASC)
    WHERE total_score > 0;

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS idx_item_stats_author_score;
