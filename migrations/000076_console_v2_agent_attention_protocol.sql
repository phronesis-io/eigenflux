-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

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

UPDATE agent_attention_items
SET client_item_id = COALESCE(client_item_id, 'legacy:' || attention_id::text),
    payload_hash = COALESCE(payload_hash, md5(attention_id::text || ':' || title || ':' || summary)),
    body = CASE WHEN body = '' THEN summary ELSE body END,
    source_ref = CASE WHEN source_ref = '{}'::jsonb
        THEN jsonb_build_object('type', source_type, 'id', source_id::text)
        ELSE source_ref END,
    actions_snapshot = CASE WHEN actions_snapshot = '[]'::jsonb
        THEN proposed_actions ELSE actions_snapshot END,
    generated_at = COALESCE(generated_at, created_at),
    updated_at = COALESCE(updated_at, created_at)
WHERE client_item_id IS NULL OR payload_hash IS NULL OR generated_at IS NULL OR updated_at IS NULL
   OR source_ref = '{}'::jsonb OR actions_snapshot = '[]'::jsonb;

ALTER TABLE agent_attention_items
    ALTER COLUMN client_item_id SET NOT NULL,
    ALTER COLUMN payload_hash SET NOT NULL,
    ALTER COLUMN generated_at SET NOT NULL,
    ALTER COLUMN updated_at SET NOT NULL,
    DROP CONSTRAINT IF EXISTS chk_agent_attention_items_status,
    ADD CONSTRAINT chk_agent_attention_items_status
        CHECK (status IN ('open', 'selected', 'pending', 'acted', 'dismissed', 'expired')) NOT VALID,
    ADD CONSTRAINT chk_agent_attention_protocol_json
        CHECK (jsonb_typeof(source_ref) = 'object'
            AND jsonb_typeof(context_ref) = 'object'
            AND jsonb_typeof(actions_snapshot) = 'array') NOT VALID,
    ADD CONSTRAINT chk_agent_attention_protocol_kind
        CHECK (producer IN ('legacy', 'agent')
            AND ((surface = 'participation' AND category IN
                    ('action_recommendation', 'goal_calibration', 'intent_update', 'other_decision'))
                OR (surface = 'focus' AND category IN
                    ('important_signal', 'opportunity', 'relationship_created', 'relationship_feedback',
                     'watch_update', 'other_attention')))
            AND response_status IN ('none', 'pending', 'completed', 'failed')) NOT VALID,
    ADD CONSTRAINT chk_agent_attention_protocol_actions
        CHECK (producer <> 'agent' OR jsonb_array_length(actions_snapshot) BETWEEN 1 AND 5) NOT VALID,
    ADD CONSTRAINT chk_agent_attention_protocol_revision
        CHECK (item_revision > 0) NOT VALID;

ALTER TABLE agent_attention_items VALIDATE CONSTRAINT chk_agent_attention_items_status;
ALTER TABLE agent_attention_items VALIDATE CONSTRAINT chk_agent_attention_protocol_json;
ALTER TABLE agent_attention_items VALIDATE CONSTRAINT chk_agent_attention_protocol_kind;
ALTER TABLE agent_attention_items VALIDATE CONSTRAINT chk_agent_attention_protocol_actions;
ALTER TABLE agent_attention_items VALIDATE CONSTRAINT chk_agent_attention_protocol_revision;

CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_attention_client_item
    ON agent_attention_items(agent_id, client_item_id);
DROP INDEX IF EXISTS uq_agent_attention_open_source;
CREATE UNIQUE INDEX uq_agent_attention_open_source
    ON agent_attention_items(agent_id, source_type, source_id)
    WHERE producer = 'legacy' AND status = 'open';
CREATE INDEX IF NOT EXISTS idx_agent_attention_agent_surface_recent
    ON agent_attention_items(agent_id, producer, created_at DESC, attention_id DESC)
    INCLUDE (surface);
CREATE INDEX IF NOT EXISTS idx_agent_attention_redaction
    ON agent_attention_items(generated_at, attention_id)
    WHERE producer = 'agent' AND redacted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_agent_attention_protocol_expiry
    ON agent_attention_items(expires_at, attention_id)
    WHERE producer = 'agent' AND status IN ('open', 'selected', 'pending');
CREATE INDEX IF NOT EXISTS idx_agent_commands_live_expiry
    ON agent_commands(created_at, command_id)
    WHERE status IN ('pending', 'notified', 'claimed');

UPDATE agent_credential_sessions session
SET scopes = array_append(session.scopes, 'attention:write')
WHERE session.revoked_at IS NULL
  AND NOT ('attention:write' = ANY(session.scopes))
  AND EXISTS (
      SELECT 1 FROM agent_principals principal
      JOIN agent_onboarding_v2 onboarding ON onboarding.agent_id = principal.agent_id
      WHERE principal.principal_id = session.principal_id
        AND principal.revoked_at IS NULL
        AND onboarding.state = 'completed'
  );

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_attention_items WHERE producer = 'agent' LIMIT 1) THEN
        RAISE EXCEPTION 'unsafe Agent Attention protocol rollback: disable the feature flag instead';
    END IF;
END $$;
-- +goose StatementEnd

UPDATE agent_credential_sessions
SET scopes = array_remove(scopes, 'attention:write')
WHERE 'attention:write' = ANY(scopes);

UPDATE agent_attention_items SET status = 'open' WHERE status IN ('selected', 'pending');

DROP INDEX IF EXISTS idx_agent_attention_redaction;
DROP INDEX IF EXISTS idx_agent_commands_live_expiry;
DROP INDEX IF EXISTS idx_agent_attention_protocol_expiry;
DROP INDEX IF EXISTS idx_agent_attention_agent_surface_recent;
DROP INDEX IF EXISTS uq_agent_attention_client_item;
DROP INDEX IF EXISTS uq_agent_attention_open_source;
CREATE UNIQUE INDEX uq_agent_attention_open_source
    ON agent_attention_items(agent_id, source_type, source_id)
    WHERE status = 'open';

ALTER TABLE agent_attention_items
    DROP CONSTRAINT IF EXISTS chk_agent_attention_protocol_revision,
    DROP CONSTRAINT IF EXISTS chk_agent_attention_protocol_actions,
    DROP CONSTRAINT IF EXISTS chk_agent_attention_protocol_kind,
    DROP CONSTRAINT IF EXISTS chk_agent_attention_protocol_json,
    DROP CONSTRAINT IF EXISTS chk_agent_attention_items_status,
    ADD CONSTRAINT chk_agent_attention_items_status
        CHECK (status IN ('open', 'acted', 'dismissed', 'expired')),
    DROP COLUMN IF EXISTS redacted_at,
    DROP COLUMN IF EXISTS responded_at,
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS generated_at,
    DROP COLUMN IF EXISTS response_status,
    DROP COLUMN IF EXISTS selected_action_key,
    DROP COLUMN IF EXISTS item_revision,
    DROP COLUMN IF EXISTS actions_snapshot,
    DROP COLUMN IF EXISTS context_ref,
    DROP COLUMN IF EXISTS source_ref,
    DROP COLUMN IF EXISTS recommendation,
    DROP COLUMN IF EXISTS body,
    DROP COLUMN IF EXISTS language,
    DROP COLUMN IF EXISTS payload_hash,
    DROP COLUMN IF EXISTS client_item_id,
    DROP COLUMN IF EXISTS category,
    DROP COLUMN IF EXISTS surface,
    DROP COLUMN IF EXISTS producer;
