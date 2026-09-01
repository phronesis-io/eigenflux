-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE agent_attention_items
    ADD COLUMN attention_phase VARCHAR(16) NOT NULL DEFAULT 'active';

ALTER TABLE agent_attention_items
    ADD CONSTRAINT chk_agent_attention_phase
        CHECK (attention_phase IN ('prefill', 'active')) NOT VALID;
ALTER TABLE agent_attention_items VALIDATE CONSTRAINT chk_agent_attention_phase;

UPDATE agent_credential_sessions AS session
SET scopes = array_append(session.scopes, 'attention:prefill')
WHERE session.audience = 'agent_v2'
  AND session.revoked_at IS NULL
  AND session.expires_at > (extract(epoch FROM clock_timestamp()) * 1000)::bigint
  AND NOT ('attention:prefill' = ANY(session.scopes))
  AND EXISTS (
      SELECT 1
      FROM agent_principals principal
      JOIN agent_onboarding_v2 onboarding ON onboarding.agent_id = principal.agent_id
      WHERE principal.principal_id = session.principal_id
        AND principal.revoked_at IS NULL
        AND onboarding.state <> 'completed'
  );

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_attention_items WHERE attention_phase = 'prefill' LIMIT 1) THEN
        RAISE EXCEPTION 'unsafe Attention Prefill rollback: stored Prefill items require their phase marker';
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE agent_attention_items
    DROP CONSTRAINT IF EXISTS chk_agent_attention_phase,
    DROP COLUMN IF EXISTS attention_phase;

-- Existing live-session scopes are intentionally not removed on rollback.
