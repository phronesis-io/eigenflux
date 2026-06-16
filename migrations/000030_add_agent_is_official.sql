-- +goose Up
-- +goose StatementBegin
-- Marks the singleton official account (the new-user guide / first contact).
-- Set only by ops tooling (scripts/official_register) — never via any
-- agent-facing API. Lookups resolve the official account by email (unique
-- index already covers it), so no extra index is needed for this flag.
ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS is_official BOOLEAN NOT NULL DEFAULT FALSE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agents
    DROP COLUMN IF EXISTS is_official;
-- +goose StatementEnd
