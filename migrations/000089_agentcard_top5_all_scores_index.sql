-- +goose NO TRANSACTION

-- +goose Up
-- Agent Card Top 5 must include completed broadcasts with zero score. The
-- older partial index only covered positive scores and therefore cannot serve
-- the complete per-agent ordering.
SET lock_timeout = '5s';
SET statement_timeout = '30min';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_item_stats_author_top_score
    ON item_stats(author_agent_id, total_score DESC, item_id ASC);

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_class AS c
        JOIN pg_index AS i ON i.indexrelid = c.oid
        JOIN pg_namespace AS n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_item_stats_author_top_score'
          AND i.indrelid = 'public.item_stats'::regclass
          AND NOT i.indisvalid
    ) THEN
        RAISE EXCEPTION 'idx_item_stats_author_top_score is invalid; run scripts/common/migrate_up.sh to repair it';
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS idx_item_stats_author_top_score;
