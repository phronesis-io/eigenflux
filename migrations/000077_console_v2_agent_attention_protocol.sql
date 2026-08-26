-- +goose NO TRANSACTION
-- +goose Up

-- Expand only. Keep the protocol flag disabled until 000079 and 000080 have
-- completed and every application instance understands agent_attention.v1.
SET lock_timeout = '2s';
SET statement_timeout = '5min';

ALTER TABLE agent_attention_items
    ADD COLUMN IF NOT EXISTS producer VARCHAR(16) NOT NULL DEFAULT 'legacy',
    ADD COLUMN IF NOT EXISTS protocol_version VARCHAR(32) NOT NULL DEFAULT 'legacy_attention.v0',
    ADD COLUMN IF NOT EXISTS surface VARCHAR(24) NOT NULL DEFAULT 'focus',
    ADD COLUMN IF NOT EXISTS category VARCHAR(32) NOT NULL DEFAULT 'other_attention',
    ADD COLUMN IF NOT EXISTS client_item_id VARCHAR(128) NULL,
    ADD COLUMN IF NOT EXISTS payload_hash VARCHAR(128) NULL,
    ADD COLUMN IF NOT EXISTS language VARCHAR(16) NOT NULL DEFAULT 'en',
    ADD COLUMN IF NOT EXISTS body TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS recommendation TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS source_ref JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS context_ref JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS actions_snapshot JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS item_revision BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS selected_action_key VARCHAR(128) NULL,
    ADD COLUMN IF NOT EXISTS response_status VARCHAR(16) NOT NULL DEFAULT 'none',
    ADD COLUMN IF NOT EXISTS generated_at BIGINT NULL,
    ADD COLUMN IF NOT EXISTS updated_at BIGINT NULL,
    ADD COLUMN IF NOT EXISTS responded_at BIGINT NULL,
    ADD COLUMN IF NOT EXISTS redacted_at BIGINT NULL;

-- The legacy physical column stays only so pre-rollout readers can start while
-- the flag is disabled. v1 never returns or populates it.
ALTER TABLE agent_attention_items ALTER COLUMN summary SET DEFAULT '';

-- This is deliberately a hard failure, not a compatibility shim. Old writers
-- omit producer and therefore receive the stable error below via the legacy
-- default. agent_attention.v1 writers always set producer = 'agent'.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reject_legacy_attention_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.producer IS DISTINCT FROM 'agent'
       OR NEW.protocol_version IS DISTINCT FROM 'agent_attention.v1' THEN
        RAISE EXCEPTION USING
            ERRCODE = 'P0001',
            MESSAGE = 'LEGACY_ATTENTION_WRITE_REJECTED',
            DETAIL = 'Only schema_version agent_attention.v1 is accepted.';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_trigger
        WHERE tgrelid = 'agent_attention_items'::regclass
          AND tgname = 'trg_reject_legacy_attention_write'
          AND NOT tgisinternal
    ) THEN
        CREATE TRIGGER trg_reject_legacy_attention_write
            BEFORE INSERT OR UPDATE ON agent_attention_items
            FOR EACH ROW EXECUTE FUNCTION reject_legacy_attention_write();
    END IF;
END $$;
-- +goose StatementEnd

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_agent_attention_client_item
    ON agent_attention_items(agent_id, client_item_id)
    WHERE producer = 'agent';
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_agent_attention_legacy_open_source
    ON agent_attention_items(agent_id, source_type, source_id)
    WHERE producer = 'legacy' AND status = 'open';
DROP INDEX CONCURRENTLY IF EXISTS uq_agent_attention_open_source;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_attention_agent_surface_recent
    ON agent_attention_items(agent_id, surface, created_at DESC, attention_id DESC)
    WHERE producer = 'agent';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_attention_redaction
    ON agent_attention_items(generated_at, attention_id)
    WHERE producer = 'agent' AND redacted_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_attention_protocol_expiry
    ON agent_attention_items(expires_at, attention_id)
    WHERE producer = 'agent' AND status IN ('open', 'selected', 'pending');
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_commands_live_expiry
    ON agent_commands(created_at, command_id)
    INCLUDE (claim_until)
    WHERE status IN ('pending', 'notified', 'claimed');
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_commands_attention_receipt
    ON agent_commands(agent_id, attention_id, created_at DESC, command_id DESC)
    INCLUDE (status, completed_at)
    WHERE attention_id IS NOT NULL AND command_type = 'attention_response';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_control_wakeup_pending_entity
    ON control_wakeup_outbox(event_type, entity_id, outbox_id)
    WHERE status = 'pending';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conversations_attention_source_replies
    ON conversations(origin_type, origin_id, status, conv_id)
    INCLUDE (participant_a, participant_b)
    WHERE origin_id > 0;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conversations_attention_reply_participant_a
    ON conversations(origin_id, participant_a, conv_id)
    WHERE origin_type = 'broadcast' AND status = 0 AND origin_id > 0;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conversations_attention_reply_participant_b
    ON conversations(origin_id, participant_b, conv_id)
    WHERE origin_type = 'broadcast' AND status = 0 AND origin_id > 0;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'irreversible Agent Attention expansion: keep the flag disabled and roll forward';
END $$;
-- +goose StatementEnd
