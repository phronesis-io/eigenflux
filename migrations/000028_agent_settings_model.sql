-- +goose Up
-- +goose StatementBegin
-- Raw X-Client-Model of the agent's runtime (e.g. "claude-opus-4-8",
-- "gpt-5"), reported alongside mode/client_host so the console can show which
-- model the agent is actually running. Refreshed on the nightly settings push
-- (and on change) the same way client_host is.
ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS model TEXT DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS model;
-- +goose StatementEnd
