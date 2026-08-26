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

-- A short ID may be assigned once to a legacy NULL row during backfill, but it
-- must never rotate after becoming public. This protects copied handles and
-- public Agent Card URLs even if a future internal writer bypasses the API.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_agent_short_id_immutable()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.short_id IS NOT NULL AND NEW.short_id IS DISTINCT FROM OLD.short_id THEN
        RAISE EXCEPTION 'agent short_id is immutable'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_trigger
        WHERE tgname = 'trg_agents_short_id_immutable'
          AND tgrelid = 'agents'::regclass
          AND NOT tgisinternal
    ) THEN
        CREATE TRIGGER trg_agents_short_id_immutable
            BEFORE UPDATE OF short_id ON agents
            FOR EACH ROW EXECUTE FUNCTION enforce_agent_short_id_immutable();
    END IF;
END $$;
-- +goose StatementEnd

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

-- A published short ID and an invite revocation are permanent identity/audit
-- data. Production rollback must use feature flags and keep these columns.
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'short IDs and invite revocation history are permanent; disable the feature instead';
END $$;
-- +goose StatementEnd

DROP INDEX CONCURRENTLY IF EXISTS uq_agents_short_id_partial;
DROP TRIGGER IF EXISTS trg_agents_short_id_immutable ON agents;
DROP FUNCTION IF EXISTS enforce_agent_short_id_immutable();
ALTER TABLE agents DROP CONSTRAINT IF EXISTS chk_agents_short_id_format;
ALTER TABLE agents DROP COLUMN IF EXISTS short_id;
ALTER TABLE invite_codes DROP COLUMN IF EXISTS revoked_at;
