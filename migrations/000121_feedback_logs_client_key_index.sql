-- +goose NO TRANSACTION

-- +goose Up
SET lock_timeout = '5s';
SET statement_timeout = '30min';

-- Empty keys preserve legacy event volume. Named keys deduplicate per Agent.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_feedback_logs_agent_client_key
    ON feedback_logs (agent_id, client_key) WHERE client_key <> '';

-- +goose StatementBegin
DO $$
DECLARE
    definition TEXT;
    valid BOOLEAN;
    ready BOOLEAN;
    access_method TEXT;
    key_count INTEGER;
    attribute_count INTEGER;
    unique_index BOOLEAN;
    predicate TEXT;
    first_key TEXT;
    second_key TEXT;
BEGIN
    SELECT pg_get_indexdef(c.oid), i.indisvalid, i.indisready, am.amname,
           i.indnkeyatts, i.indnatts, i.indisunique, pg_get_expr(i.indpred, i.indrelid),
           pg_get_indexdef(c.oid, 1, TRUE), pg_get_indexdef(c.oid, 2, TRUE)
      INTO definition, valid, ready, access_method, key_count,
           attribute_count, unique_index, predicate, first_key, second_key
      FROM pg_class AS c
      JOIN pg_namespace AS n ON n.oid = c.relnamespace
      JOIN pg_index AS i ON i.indexrelid = c.oid
      JOIN pg_am AS am ON am.oid = c.relam
     WHERE n.nspname = 'public'
       AND c.relname = 'uq_feedback_logs_agent_client_key'
       AND i.indrelid = 'public.feedback_logs'::regclass;

    IF definition IS NULL OR NOT valid OR NOT ready
       OR access_method <> 'btree'
       OR key_count <> 2 OR attribute_count <> 2
       OR NOT unique_index
       OR predicate IS DISTINCT FROM '((client_key)::text <> ''''::text)'
       OR first_key <> 'agent_id'
       OR second_key <> 'client_key' THEN
        RAISE EXCEPTION 'uq_feedback_logs_agent_client_key is missing, invalid, or has the wrong definition: %. For an invalid index run scripts/common/migrate_up.sh; for a valid wrong-definition index run DROP INDEX CONCURRENTLY public.uq_feedback_logs_agent_client_key, then rerun the migration', definition;
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS uq_feedback_logs_agent_client_key;
