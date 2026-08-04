-- +goose Up
-- A Redis lease is only advisory. The per-rebuild sequence is a durable
-- fencing token: a holder that resumes after its lease expired cannot overwrite
-- a projection accepted by a newer holder.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE agent_cards
    ADD COLUMN rebuild_fence BIGINT NOT NULL DEFAULT 0;

-- Sequence cache must remain 1. PostgreSQL caches ranges per connection, so a
-- larger cache does not preserve nextval order across pooled connections.
CREATE SEQUENCE agent_card_rebuild_fence_seq AS BIGINT START WITH 1 CACHE 1;

-- During a rolling deployment, an old binary does not mention rebuild_fence
-- in its UPSERT. Reject any visible projection change that does not advance
-- the fence, so mixed-version writers fail closed instead of reverting a row.
-- Fence equality specifically identifies an old writer: every new writer
-- supplies a fresh sequence value even when source_version advances.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_agent_card_rebuild_fence()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.rebuild_fence > 0
       AND (NEW.public_card IS DISTINCT FROM OLD.public_card
        OR NEW.private_card IS DISTINCT FROM OLD.private_card
        OR NEW.schema_version IS DISTINCT FROM OLD.schema_version
        OR NEW.source_version IS DISTINCT FROM OLD.source_version
        OR NEW.card_version IS DISTINCT FROM OLD.card_version
        OR NEW.generated_at IS DISTINCT FROM OLD.generated_at)
       AND (NEW.rebuild_fence = OLD.rebuild_fence
            OR NEW.source_version < OLD.source_version
            OR (NEW.source_version = OLD.source_version
                AND NEW.rebuild_fence < OLD.rebuild_fence)) THEN
        RAISE EXCEPTION 'agent card rebuild ordering key did not advance'
            USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_agent_card_rebuild_fence
BEFORE UPDATE ON agent_cards
FOR EACH ROW EXECUTE FUNCTION enforce_agent_card_rebuild_fence();

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP TRIGGER IF EXISTS trg_agent_card_rebuild_fence ON agent_cards;
DROP FUNCTION IF EXISTS enforce_agent_card_rebuild_fence();
DROP SEQUENCE IF EXISTS agent_card_rebuild_fence_seq;

ALTER TABLE agent_cards
    DROP COLUMN IF EXISTS rebuild_fence;
