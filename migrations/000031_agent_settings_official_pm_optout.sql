-- +goose Up
-- +goose StatementBegin
-- Per-user opt-out for proactive official-account PMs (#4 topic recommendation,
-- #5 network-wide trending). Set by the user via `eigenflux config set --key
-- official_pm_optout --value true`; the cron senders skip opted-out agents.
ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS official_pm_optout BOOLEAN NOT NULL DEFAULT FALSE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS official_pm_optout;
-- +goose StatementEnd
