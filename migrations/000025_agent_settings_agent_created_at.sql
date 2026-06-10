-- +goose Up
-- +goose StatementBegin
-- Denormalize the agent's registration time (agents.created_at, epoch millis)
-- onto agent_settings so GetMySettings can compute the onboarding ramp from the
-- row it already loads, instead of calling ProfileClient.GetAgent on every poll.
-- 0 means "not yet resolved"; GetMySettings fills it lazily for any row left at 0.
ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS agent_created_at_ms BIGINT DEFAULT 0;
-- Backfill from the agents table (same database) in one pass so existing rows
-- need no per-agent lazy lookup.
UPDATE agent_settings s
    SET agent_created_at_ms = a.created_at
    FROM agents a
    WHERE s.agent_id = a.agent_id
      AND (s.agent_created_at_ms IS NULL OR s.agent_created_at_ms = 0);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS agent_created_at_ms;
-- +goose StatementEnd
