-- +goose Up
-- +goose StatementBegin
-- The daily profile-change cleanup scans by retention cutoff. Keep this index
-- separate from (agent_id, created_at DESC): the latter serves refresh-context
-- but cannot efficiently find old rows across all agents.
CREATE INDEX idx_agent_profile_events_created
    ON agent_profile_change_events(created_at, id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_agent_profile_events_created;
-- +goose StatementEnd
