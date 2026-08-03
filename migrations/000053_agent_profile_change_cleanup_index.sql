-- +goose Up
-- The table has only a small amount of production data at rollout time, so a
-- regular transactional index build is preferable here: if any later
-- validation fails, goose rolls the entire migration back and the next run is
-- safe. Short lock/statement timeouts prevent this migration from queueing
-- behind a busy writer indefinitely.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- The daily profile-change cleanup scans by retention cutoff. Keep this index
-- separate from (agent_id, created_at DESC): the latter serves refresh-context
-- but cannot efficiently find old rows across all agents.
CREATE INDEX IF NOT EXISTS idx_agent_profile_events_created
    ON agent_profile_change_events(created_at, id);

-- Cleanup expands changed_paths as an array. Reject any future row with a
-- different JSON shape; validate existing rows during migration while the
-- table is still small.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_agent_profile_events_changed_paths_array'
          AND conrelid = 'agent_profile_change_events'::regclass
    ) THEN
        ALTER TABLE agent_profile_change_events
            ADD CONSTRAINT chk_agent_profile_events_changed_paths_array
            CHECK (jsonb_typeof(changed_paths) = 'array') NOT VALID;
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE agent_profile_change_events
    VALIDATE CONSTRAINT chk_agent_profile_events_changed_paths_array;

-- Production was verified to contain no orphan rows before this migration.
-- Keep audit PII inside the account lifecycle from now on: deleting an agent
-- must delete its profile history instead of leaving permanent orphan data.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'fk_agent_profile_events_agent'
          AND conrelid = 'agent_profile_change_events'::regclass
    ) THEN
        ALTER TABLE agent_profile_change_events
            ADD CONSTRAINT fk_agent_profile_events_agent
            FOREIGN KEY (agent_id) REFERENCES agents(agent_id) ON DELETE CASCADE NOT VALID;
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE agent_profile_change_events
    VALIDATE CONSTRAINT fk_agent_profile_events_agent;
-- +goose Down
ALTER TABLE agent_profile_change_events
    DROP CONSTRAINT IF EXISTS fk_agent_profile_events_agent;
ALTER TABLE agent_profile_change_events
    DROP CONSTRAINT IF EXISTS chk_agent_profile_events_changed_paths_array;
DROP INDEX IF EXISTS idx_agent_profile_events_created;
