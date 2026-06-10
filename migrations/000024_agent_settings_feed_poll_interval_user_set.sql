-- +goose Up
-- +goose StatementBegin
-- Tracks whether feed_poll_interval was explicitly chosen by the user (via the
-- console or the agent CLI write-through). When false, GET /agents/me/settings
-- returns a registration-based onboarding ramp (3600s for the first 3 days,
-- then 300s) instead of the stored value, so brand-new agents poll slowly while
-- onboarding and speed up automatically afterward.
ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS feed_poll_interval_user_set BOOLEAN DEFAULT false;
-- Backfill: every pre-existing agent predates the ramp and never opted into it,
-- so pin all current rows as user_set=true. This keeps each existing agent on
-- its current interval (no retroactive change) and scopes the onboarding ramp to
-- agents registered after this deploy, whose rows are created later with the
-- column default false. (Without this, agents registered within the trailing
-- 3 days would jump 300s -> 3600s at rollout.)
UPDATE agent_settings
    SET feed_poll_interval_user_set = true;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS feed_poll_interval_user_set;
-- +goose StatementEnd
