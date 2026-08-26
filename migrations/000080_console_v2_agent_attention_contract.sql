-- +goose Up
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM agent_attention_items
        WHERE producer <> 'agent' OR protocol_version <> 'agent_attention.v1'
        LIMIT 1
    ) THEN
        RAISE EXCEPTION 'legacy Attention rows remain; 000079 must finish before contract';
    END IF;
    IF EXISTS (
        SELECT 1 FROM agent_attention_items
        WHERE client_item_id IS NULL OR payload_hash IS NULL
           OR generated_at IS NULL OR updated_at IS NULL
        LIMIT 1
    ) THEN
        RAISE EXCEPTION 'invalid agent_attention.v1 rows prevent contract migration';
    END IF;
    IF EXISTS (
        SELECT 1 FROM agent_commands
        WHERE command_type = 'attention_response'
          AND payload->>'protocol_version' IS DISTINCT FROM 'agent_attention.v1'
        LIMIT 1
    ) THEN
        RAISE EXCEPTION 'invalid agent_attention.v1 command payload prevents contract migration';
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE agent_attention_items
    DROP CONSTRAINT IF EXISTS chk_agent_attention_items_status,
    ADD CONSTRAINT chk_agent_attention_items_status
        CHECK (status IN ('open', 'selected', 'pending', 'acted', 'dismissed', 'expired')) NOT VALID,
    ADD CONSTRAINT chk_agent_attention_protocol_json
        CHECK (jsonb_typeof(source_ref) = 'object'
            AND jsonb_typeof(context_ref) = 'object'
            AND jsonb_typeof(actions_snapshot) = 'array') NOT VALID,
    ADD CONSTRAINT chk_agent_attention_protocol_kind
        CHECK (producer = 'agent'
            AND protocol_version = 'agent_attention.v1'
            AND ((surface = 'participation' AND category IN
                    ('action_recommendation', 'goal_calibration', 'intent_update', 'other_decision'))
                OR (surface = 'focus' AND category IN
                    ('important_signal', 'opportunity', 'relationship_created', 'relationship_feedback',
                     'watch_update', 'other_attention')))
            AND response_status IN ('none', 'pending', 'completed', 'failed')) NOT VALID,
    ADD CONSTRAINT chk_agent_attention_protocol_actions
        CHECK (jsonb_array_length(actions_snapshot) BETWEEN 1 AND 5) NOT VALID,
    ADD CONSTRAINT chk_agent_attention_protocol_revision
        CHECK (item_revision > 0) NOT VALID,
    ADD CONSTRAINT chk_agent_attention_protocol_required
        CHECK (client_item_id IS NOT NULL AND payload_hash IS NOT NULL
            AND generated_at IS NOT NULL AND updated_at IS NOT NULL) NOT VALID;

ALTER TABLE agent_attention_items VALIDATE CONSTRAINT chk_agent_attention_items_status;
ALTER TABLE agent_attention_items VALIDATE CONSTRAINT chk_agent_attention_protocol_json;
ALTER TABLE agent_attention_items VALIDATE CONSTRAINT chk_agent_attention_protocol_kind;
ALTER TABLE agent_attention_items VALIDATE CONSTRAINT chk_agent_attention_protocol_actions;
ALTER TABLE agent_attention_items VALIDATE CONSTRAINT chk_agent_attention_protocol_revision;
ALTER TABLE agent_attention_items VALIDATE CONSTRAINT chk_agent_attention_protocol_required;

ALTER TABLE agent_commands
    ADD CONSTRAINT chk_agent_commands_attention_protocol
        CHECK (command_type <> 'attention_response'
            OR payload->>'protocol_version' = 'agent_attention.v1') NOT VALID;
ALTER TABLE agent_commands VALIDATE CONSTRAINT chk_agent_commands_attention_protocol;

ALTER TABLE agent_attention_items
    ALTER COLUMN client_item_id SET NOT NULL,
    ALTER COLUMN payload_hash SET NOT NULL,
    ALTER COLUMN generated_at SET NOT NULL,
    ALTER COLUMN updated_at SET NOT NULL;

DROP INDEX IF EXISTS uq_agent_attention_legacy_open_source;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'irreversible agent_attention.v1 contract: disable the feature flag and roll forward';
END $$;
-- +goose StatementEnd
