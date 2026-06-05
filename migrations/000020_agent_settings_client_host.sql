-- +goose Up
-- +goose StatementBegin
-- Raw X-Client-Host of the agent's runtime (e.g. "openclaw/0.0.13",
-- "claude-code/0.0.5"), persisted alongside the derived mode so the console
-- can show which host plugin the agent actually runs in.
ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS client_host TEXT DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS client_host;
-- +goose StatementEnd
