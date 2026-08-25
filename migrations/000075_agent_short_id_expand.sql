-- +goose NO TRANSACTION
-- +goose Up
SET lock_timeout = '5s';
SET statement_timeout = '30min';

-- Public Agent IDs are opaque, case-sensitive, five-letter handles. The
-- column remains nullable during the expand/backfill phase so this migration
-- never rewrites the existing agents table or blocks rolling deployment.
ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS short_id VARCHAR(5) COLLATE "C";

-- Historical personal EFI codes remain readable during the compatibility
-- window, but compromised codes must be revocable without deleting audit and
-- attribution history.
ALTER TABLE invite_codes
    ADD COLUMN IF NOT EXISTS revoked_at BIGINT NULL;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'chk_agents_short_id_format'
    ) THEN
        ALTER TABLE agents
            ADD CONSTRAINT chk_agents_short_id_format
            CHECK (short_id IS NULL OR short_id ~ '^[A-Za-z]{5}$') NOT VALID;
    END IF;
END $$;
-- +goose StatementEnd

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_agents_short_id_partial
    ON agents(short_id)
    WHERE short_id IS NOT NULL;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'uq_agents_short_id_partial'
          AND i.indisvalid
          AND i.indisready
    ) THEN
        RAISE EXCEPTION 'short-id index is missing or invalid';
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS uq_agents_short_id_partial;
ALTER TABLE agents DROP CONSTRAINT IF EXISTS chk_agents_short_id_format;
ALTER TABLE agents DROP COLUMN IF EXISTS short_id;
ALTER TABLE invite_codes DROP COLUMN IF EXISTS revoked_at;
