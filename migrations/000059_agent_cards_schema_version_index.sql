-- +goose NO TRANSACTION

-- +goose Up
-- The projection is continuously rebuilt in production. Build this scan index
-- without blocking card upserts while PostgreSQL reads the existing table.
SET lock_timeout = '5s';
SET statement_timeout = '30min';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_cards_schema_version_agent
    ON agent_cards(schema_version, agent_id);

-- An interrupted concurrent build can leave an invalid index behind. Refuse
-- to mark the migration complete so operators can remove it and retry safely.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class AS c
        JOIN pg_index AS i ON i.indexrelid = c.oid
        JOIN pg_namespace AS n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_agent_cards_schema_version_agent'
          AND i.indrelid = 'public.agent_cards'::regclass
          AND NOT i.indisvalid
    ) THEN
        RAISE EXCEPTION 'idx_agent_cards_schema_version_agent is invalid; drop it concurrently and rerun migration 59';
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS idx_agent_cards_schema_version_agent;
