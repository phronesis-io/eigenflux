-- +goose NO TRANSACTION

-- +goose Up
SET lock_timeout = '5s';
SET statement_timeout = '30min';

-- This expression index keeps exact-duplicate verification bounded for prolific
-- authors without storing a second copy of the content hash.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_raw_items_author_content_md5
    ON raw_items (author_agent_id, md5(raw_content));

-- +goose StatementBegin
DO $$
DECLARE
    definition TEXT;
    valid BOOLEAN;
    ready BOOLEAN;
    access_method TEXT;
    key_count INTEGER;
    attribute_count INTEGER;
    no_predicate BOOLEAN;
    first_key TEXT;
    second_key TEXT;
BEGIN
    SELECT pg_get_indexdef(c.oid), i.indisvalid, i.indisready, am.amname,
           i.indnkeyatts, i.indnatts, i.indpred IS NULL,
           pg_get_indexdef(c.oid, 1, TRUE), pg_get_indexdef(c.oid, 2, TRUE)
      INTO definition, valid, ready, access_method, key_count,
           attribute_count, no_predicate, first_key, second_key
      FROM pg_class AS c
      JOIN pg_namespace AS n ON n.oid = c.relnamespace
      JOIN pg_index AS i ON i.indexrelid = c.oid
      JOIN pg_am AS am ON am.oid = c.relam
     WHERE n.nspname = 'public'
       AND c.relname = 'idx_raw_items_author_content_md5'
       AND i.indrelid = 'public.raw_items'::regclass;

    IF definition IS NULL OR NOT valid OR NOT ready
       OR access_method <> 'btree'
       OR key_count <> 2 OR attribute_count <> 2
       OR NOT no_predicate
       OR first_key <> 'author_agent_id'
       OR second_key <> 'md5(raw_content)' THEN
        RAISE EXCEPTION 'idx_raw_items_author_content_md5 is missing, invalid, or has the wrong definition: %. For an invalid index run scripts/common/migrate_up.sh; for a valid wrong-definition index run DROP INDEX CONCURRENTLY public.idx_raw_items_author_content_md5, then rerun the migration', definition;
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS idx_raw_items_author_content_md5;
