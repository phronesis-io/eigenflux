-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- Email recovery sessions need an internal principal anchor, but it must never
-- be accepted as an Agent signing key or receive an Agent credential session.
ALTER TABLE agent_principals
    DROP CONSTRAINT chk_agent_principals_key_type,
    ADD CONSTRAINT chk_agent_principals_key_type
        CHECK (key_type IN ('ed25519-v1', 'email-recovery-v1'));

ALTER TABLE console_v2_sessions
    ADD COLUMN auth_method VARCHAR(24) NOT NULL DEFAULT 'handoff',
    ADD COLUMN recent_auth_at BIGINT NULL,
    ADD CONSTRAINT chk_console_v2_sessions_auth_method
        CHECK (auth_method IN ('handoff', 'email_otp'));

ALTER TABLE agent_signature_nonces
    ADD COLUMN subject_agent_id BIGINT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    ADD COLUMN console_session_id VARCHAR(128) NULL REFERENCES console_v2_sessions(session_id) ON DELETE CASCADE;
CREATE INDEX idx_agent_signature_nonces_add_device
    ON agent_signature_nonces(subject_agent_id, console_session_id, expires_at)
    WHERE domain = 'add_device' AND consumed_at IS NULL;

-- One active ownership row per canonical email, irrespective of whether the
-- legacy owner has completed OTP yet. This closes the first-bind race at the
-- database boundary; verification still has its narrower V1-compatible index.
CREATE UNIQUE INDEX uq_agent_email_binding_active_email
    ON agent_email_bindings(normalized_email)
    WHERE status = 'active';

-- The backfill cursor is deliberately one row per job. A worker commits a
-- bounded page and advances the cursor in the same transaction, so reruns are
-- cheap and never need to rescan the full agents table.
CREATE TABLE console_v2_backfill_state (
    job_name VARCHAR(64) PRIMARY KEY,
    last_agent_id BIGINT NOT NULL DEFAULT 0,
    processed_count BIGINT NOT NULL DEFAULT 0,
    conflict_count BIGINT NOT NULL DEFAULT 0,
    completed_at BIGINT NULL,
    updated_at BIGINT NOT NULL,
    CONSTRAINT chk_console_v2_backfill_counts
        CHECK (last_agent_id >= 0 AND processed_count >= 0 AND conflict_count >= 0)
);

-- Conflicting legacy addresses are quarantined for manual resolution. The
-- backfill never guesses an owner and never merges Agents.
CREATE TABLE console_v2_email_conflicts (
    normalized_email VARCHAR(255) PRIMARY KEY,
    agent_ids JSONB NOT NULL,
    reason VARCHAR(64) NOT NULL,
    detected_at BIGINT NOT NULL,
    resolved_at BIGINT NULL,
    CONSTRAINT chk_console_v2_email_conflicts_ids CHECK (jsonb_typeof(agent_ids) = 'array'),
    CONSTRAINT chk_console_v2_email_conflicts_reason
        CHECK (reason IN ('normalized_duplicate', 'invalid_email', 'reserved_internal_alias'))
);
CREATE INDEX idx_console_v2_email_conflicts_unresolved
    ON console_v2_email_conflicts(detected_at, normalized_email)
    WHERE resolved_at IS NULL;

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_principals LIMIT 1)
       OR EXISTS (SELECT 1 FROM agents WHERE email_kind IN ('internal_alias', 'v2_bound') LIMIT 1) THEN
        RAISE EXCEPTION 'unsafe Console V2 auth rollback: disable feature flags instead of removing recovery constraints';
    END IF;
END $$;
-- +goose StatementEnd

DROP TABLE IF EXISTS console_v2_email_conflicts;
DROP TABLE IF EXISTS console_v2_backfill_state;
DROP INDEX IF EXISTS uq_agent_email_binding_active_email;

DROP INDEX IF EXISTS idx_agent_signature_nonces_add_device;
ALTER TABLE agent_signature_nonces
    DROP COLUMN IF EXISTS console_session_id,
    DROP COLUMN IF EXISTS subject_agent_id;

ALTER TABLE console_v2_sessions
    DROP CONSTRAINT IF EXISTS chk_console_v2_sessions_auth_method,
    DROP COLUMN IF EXISTS recent_auth_at,
    DROP COLUMN IF EXISTS auth_method;

-- Recovery principals exist only to anchor email-authenticated Console V2
-- sessions. Removing them cascades those sessions before restoring the
-- ed25519-only constraint, so rollback remains executable after real use.
DELETE FROM agent_principals WHERE key_type = 'email-recovery-v1';

ALTER TABLE agent_principals
    DROP CONSTRAINT chk_agent_principals_key_type,
    ADD CONSTRAINT chk_agent_principals_key_type CHECK (key_type = 'ed25519-v1');
