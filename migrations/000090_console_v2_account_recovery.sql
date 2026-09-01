-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE agents
    ADD COLUMN identity_state VARCHAR(32) NOT NULL DEFAULT 'active',
    ADD CONSTRAINT chk_agents_identity_state
        CHECK (identity_state IN ('active', 'recovered_temporary'));
CREATE INDEX idx_agents_active_identity
    ON agents(agent_id)
    WHERE identity_state = 'active';

ALTER TABLE console_v2_handoffs
    ADD COLUMN client_capabilities TEXT[] NOT NULL DEFAULT '{}'::text[],
    ADD COLUMN revoked_at BIGINT NULL;
CREATE INDEX idx_console_v2_handoffs_agent_pending
    ON console_v2_handoffs(agent_id)
    WHERE consumed_at IS NULL AND revoked_at IS NULL;
ALTER TABLE console_v2_sessions
    ADD COLUMN client_capabilities TEXT[] NOT NULL DEFAULT '{}'::text[];
ALTER TABLE agent_credential_sessions
    ADD COLUMN access_refresh_required BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE agent_account_recoveries (
    recovery_id_hash VARCHAR(128) PRIMARY KEY,
    source_agent_id BIGINT NOT NULL REFERENCES agents(agent_id),
    target_agent_id BIGINT NOT NULL REFERENCES agents(agent_id),
    principal_id BIGINT NOT NULL REFERENCES agent_principals(principal_id),
    console_session_id VARCHAR(128) NOT NULL REFERENCES console_v2_sessions(session_id),
    email_challenge_id VARCHAR(64) NOT NULL REFERENCES v2_email_challenges(challenge_id),
    normalized_email_hash VARCHAR(128) NOT NULL,
    source_disposition VARCHAR(16) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    expires_at BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    completed_at BIGINT NULL,
    result_snapshot JSONB NULL,
    CONSTRAINT chk_agent_account_recoveries_distinct
        CHECK (source_agent_id <> target_agent_id),
    CONSTRAINT chk_agent_account_recoveries_status
        CHECK (status IN ('pending', 'completed', 'expired', 'revoked')),
    CONSTRAINT chk_agent_account_recoveries_source_disposition
        CHECK (source_disposition IN ('abandon', 'preserve')),
    CONSTRAINT chk_agent_account_recoveries_result
        CHECK ((status = 'completed' AND completed_at IS NOT NULL AND result_snapshot IS NOT NULL)
            OR (status <> 'completed' AND completed_at IS NULL AND result_snapshot IS NULL)),
    CONSTRAINT chk_agent_account_recoveries_snapshot
        CHECK (result_snapshot IS NULL OR jsonb_typeof(result_snapshot) = 'object')
);
CREATE INDEX idx_agent_account_recovery_source_completed
    ON agent_account_recoveries(source_agent_id, completed_at DESC)
    WHERE status = 'completed';
CREATE INDEX idx_agent_account_recovery_expiry
    ON agent_account_recoveries(expires_at, status);

CREATE TABLE agent_account_recovery_audit (
    audit_id BIGSERIAL PRIMARY KEY,
    recovery_id_hash VARCHAR(128) NOT NULL,
    source_agent_id BIGINT NOT NULL REFERENCES agents(agent_id),
    target_agent_id BIGINT NOT NULL REFERENCES agents(agent_id),
    principal_id BIGINT NOT NULL,
    console_session_id VARCHAR(128) NOT NULL,
    result VARCHAR(24) NOT NULL,
    occurred_at BIGINT NOT NULL,
    CONSTRAINT chk_agent_account_recovery_audit_result
        CHECK (result IN ('completed', 'expired', 'rejected'))
);
CREATE INDEX idx_agent_account_recovery_audit_source
    ON agent_account_recovery_audit(source_agent_id, occurred_at DESC);

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- Identity-switch tombstones and audit records are permanent history. A
-- rollback could make an abandoned temporary Agent visible or erase the audit
-- trail for switches between preserved formal accounts.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agents WHERE identity_state = 'recovered_temporary' LIMIT 1)
       OR EXISTS (SELECT 1 FROM agent_account_recovery_audit LIMIT 1) THEN
        RAISE EXCEPTION 'account recovery history is permanent; disable the feature instead';
    END IF;
END $$;
-- +goose StatementEnd

DROP INDEX IF EXISTS idx_agent_account_recovery_audit_source;
DROP TABLE IF EXISTS agent_account_recovery_audit;
DROP INDEX IF EXISTS idx_agent_account_recovery_expiry;
DROP INDEX IF EXISTS idx_agent_account_recovery_source_completed;
DROP TABLE IF EXISTS agent_account_recoveries;
ALTER TABLE agent_credential_sessions DROP COLUMN IF EXISTS access_refresh_required;
ALTER TABLE console_v2_sessions DROP COLUMN IF EXISTS client_capabilities;
DROP INDEX IF EXISTS idx_console_v2_handoffs_agent_pending;
ALTER TABLE console_v2_handoffs DROP COLUMN IF EXISTS revoked_at;
ALTER TABLE console_v2_handoffs DROP COLUMN IF EXISTS client_capabilities;
DROP INDEX IF EXISTS idx_agents_active_identity;
ALTER TABLE agents
    DROP CONSTRAINT IF EXISTS chk_agents_identity_state,
    DROP COLUMN IF EXISTS identity_state;
