-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

UPDATE agent_today_model_briefs
SET narrative = '', status = 'failed', lease_until = 0, updated_at =
    (extract(epoch FROM clock_timestamp()) * 1000)::bigint
WHERE (language = 'zh-CN' AND char_length(narrative) > 60)
   OR (language = 'en' AND char_length(narrative) > 120);

ALTER TABLE agent_today_model_briefs
    DROP CONSTRAINT chk_agent_today_model_brief_narrative;
ALTER TABLE agent_today_model_briefs
    ADD CONSTRAINT chk_agent_today_model_brief_narrative
    CHECK ((language = 'zh-CN' AND char_length(narrative) <= 60)
        OR (language = 'en' AND char_length(narrative) <= 120));

UPDATE agent_credential_sessions AS session
SET scopes = ARRAY(
    SELECT DISTINCT scope
    FROM unnest(session.scopes || ARRAY[
        'feed:feedback', 'communication:read', 'communication:write',
        'relations:read', 'relations:write', 'broadcast:write',
        'profile:read', 'profile:write', 'settings:read', 'settings:write'
    ]::text[]) AS scope
)
WHERE session.audience = 'agent_v2'
  AND session.revoked_at IS NULL
  AND session.expires_at > (extract(epoch FROM clock_timestamp()) * 1000)::bigint
  AND EXISTS (
      SELECT 1
      FROM agent_principals principal
      JOIN agent_onboarding_v2 onboarding ON onboarding.agent_id = principal.agent_id
      WHERE principal.principal_id = session.principal_id
        AND principal.revoked_at IS NULL
        AND principal.status = 'active'
        AND onboarding.state = 'completed'
  );

-- +goose Down
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_today_model_briefs
    DROP CONSTRAINT chk_agent_today_model_brief_narrative;
ALTER TABLE agent_today_model_briefs
    ADD CONSTRAINT chk_agent_today_model_brief_narrative
    CHECK (char_length(narrative) <= 280);
