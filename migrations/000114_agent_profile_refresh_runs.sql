-- +goose Up
SET LOCAL lock_timeout = '5s';

-- Append-only health telemetry for the client-side Periodic Profile Refresh.
-- The CLI reports one `dispatched` row when it hands a refresh task to the
-- agent and one `completed` row when the agent finishes the evaluation, linked
-- by the client-generated run_id. Completion rows exist for unchanged
-- evaluations too, which agent_profile_change_events cannot represent.
CREATE TABLE agent_profile_refresh_runs (
    id             BIGSERIAL PRIMARY KEY,
    agent_id       BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    run_id         VARCHAR(64) NOT NULL,
    stage          VARCHAR(16) NOT NULL,
    outcome        VARCHAR(16),
    changed_paths  JSONB,
    trigger        VARCHAR(16) NOT NULL,
    client_host    TEXT NOT NULL DEFAULT '',
    client_mode    VARCHAR(16) NOT NULL DEFAULT '',
    cli_version    VARCHAR(32) NOT NULL DEFAULT '',
    plugin_version VARCHAR(32) NOT NULL DEFAULT '',
    created_at     BIGINT NOT NULL,
    CONSTRAINT chk_profile_refresh_runs_stage
        CHECK (stage IN ('dispatched', 'completed')),
    CONSTRAINT chk_profile_refresh_runs_trigger
        CHECK (trigger IN ('plugin_task', 'pending_line', 'manual_force', 'untracked')),
    -- Every branch is NULL-safe: a NULL CHECK result would accept the row.
    CONSTRAINT chk_profile_refresh_runs_outcome
        CHECK ((stage = 'dispatched' AND outcome IS NULL AND changed_paths IS NULL)
            OR (stage = 'completed' AND outcome IS NOT NULL AND changed_paths IS NOT NULL
                AND outcome IN ('changed', 'unchanged'))),
    CONSTRAINT chk_profile_refresh_runs_changed_paths_array
        CHECK (changed_paths IS NULL OR jsonb_typeof(changed_paths) = 'array')
);

-- Idempotency key: client retries of the same stage never add a second row.
CREATE UNIQUE INDEX uq_profile_refresh_runs_agent_run_stage
    ON agent_profile_refresh_runs(agent_id, run_id, stage);
CREATE INDEX idx_profile_refresh_runs_agent_created
    ON agent_profile_refresh_runs(agent_id, created_at DESC);
-- Weekly metrics and the 90-day retention cleanup scan by time.
CREATE INDEX idx_profile_refresh_runs_created
    ON agent_profile_refresh_runs(created_at, id);

-- +goose Down
DROP TABLE IF EXISTS agent_profile_refresh_runs;
