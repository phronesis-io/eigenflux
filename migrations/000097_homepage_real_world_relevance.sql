-- +goose NO TRANSACTION
-- +goose Up
SET lock_timeout = '5s';
SET statement_timeout = '30min';

ALTER TABLE processed_items
    ADD COLUMN IF NOT EXISTS homepage_real_world_relevant BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_processed_items_homepage_real_world_relevant
    ON processed_items (updated_at DESC, item_id DESC)
    WHERE status = 3 AND homepage_eligible = TRUE AND homepage_real_world_relevant = TRUE;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_class AS c
        JOIN pg_index AS i ON i.indexrelid = c.oid
        JOIN pg_namespace AS n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_processed_items_homepage_real_world_relevant'
          AND i.indrelid = 'public.processed_items'::regclass
          AND NOT i.indisvalid
    ) THEN
        RAISE EXCEPTION 'idx_processed_items_homepage_real_world_relevant is invalid; drop it concurrently and rerun migration 97';
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS idx_processed_items_homepage_real_world_relevant;
ALTER TABLE processed_items
    DROP COLUMN IF EXISTS homepage_real_world_relevant;
