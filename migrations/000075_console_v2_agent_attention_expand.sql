-- +goose NO TRANSACTION
-- +goose Up

-- Phase 1 is expand-only. Existing Attention readers and writers continue to
-- use the legacy columns while the agent_attention.v1 implementation remains
-- disabled. No data is deleted and no legacy write is rejected here.
SET lock_timeout = '2s';
SET statement_timeout = '5min';

ALTER TABLE agent_attention_items
    ADD COLUMN IF NOT EXISTS producer VARCHAR(16) NOT NULL DEFAULT 'legacy',
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

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_agent_attention_client_item
    ON agent_attention_items(agent_id, client_item_id)
    WHERE producer = 'agent';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_attention_agent_surface_recent
    ON agent_attention_items(agent_id, surface, created_at DESC, attention_id DESC)
    WHERE producer = 'agent';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_attention_redaction
    ON agent_attention_items(generated_at, attention_id)
    WHERE producer = 'agent' AND redacted_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_attention_protocol_expiry
    ON agent_attention_items(expires_at, attention_id)
    WHERE producer = 'agent' AND status IN ('open', 'selected', 'pending');

-- +goose Down

-- Expand-only migrations are rolled back by keeping the protocol flag off.
SELECT 1;
