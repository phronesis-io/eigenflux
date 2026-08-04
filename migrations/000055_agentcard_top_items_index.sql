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

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_class AS c
        JOIN pg_index AS i ON i.indexrelid = c.oid
        JOIN pg_namespace AS n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_item_stats_author_score'
          AND i.indrelid = 'public.item_stats'::regclass
          AND NOT i.indisvalid
    ) THEN
        RAISE EXCEPTION 'idx_item_stats_author_score is invalid; run scripts/common/migrate_up.sh to repair it';
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS idx_item_stats_author_score;
