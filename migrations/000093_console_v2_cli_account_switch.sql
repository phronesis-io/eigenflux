-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

CREATE TABLE agent_cli_account_switches (
    switch_id_hash VARCHAR(128) PRIMARY KEY,
    source_agent_id BIGINT NOT NULL REFERENCES agents(agent_id),
    target_agent_id BIGINT NULL REFERENCES agents(agent_id),
    principal_id BIGINT NOT NULL REFERENCES agent_principals(principal_id),
    source_console_session_id VARCHAR(128) NOT NULL REFERENCES console_v2_sessions(session_id),
    target_console_session_id VARCHAR(128) NULL REFERENCES console_v2_sessions(session_id),
    status VARCHAR(24) NOT NULL DEFAULT 'pending_target',
    ownership_verified_at BIGINT NULL,
    expires_at BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    completed_at BIGINT NULL,
    CONSTRAINT chk_agent_cli_account_switches_distinct
        CHECK (target_agent_id IS NULL OR source_agent_id <> target_agent_id),
    CONSTRAINT chk_agent_cli_account_switches_status
        CHECK (status IN ('pending_target', 'pending_onboarding', 'completed', 'expired', 'revoked')),
    CONSTRAINT chk_agent_cli_account_switches_target
        CHECK ((status = 'pending_target' AND target_agent_id IS NULL AND target_console_session_id IS NULL)
            OR (status IN ('pending_onboarding', 'completed') AND target_agent_id IS NOT NULL AND target_console_session_id IS NOT NULL)
            OR status IN ('expired', 'revoked')),
    CONSTRAINT chk_agent_cli_account_switches_completion
        CHECK ((status = 'completed' AND completed_at IS NOT NULL)
            OR (status <> 'completed' AND completed_at IS NULL))
);
CREATE UNIQUE INDEX idx_agent_cli_account_switches_principal_pending
    ON agent_cli_account_switches(principal_id)
    WHERE status IN ('pending_target', 'pending_onboarding');
CREATE INDEX idx_agent_cli_account_switches_target_pending
    ON agent_cli_account_switches(target_agent_id, target_console_session_id)
    WHERE status = 'pending_onboarding';
CREATE INDEX idx_agent_cli_account_switches_expiry
    ON agent_cli_account_switches(expires_at, status);

CREATE TABLE agent_cli_account_switch_audit (
    audit_id BIGSERIAL PRIMARY KEY,
    switch_id_hash VARCHAR(128) NOT NULL,
    source_agent_id BIGINT NOT NULL REFERENCES agents(agent_id),
    target_agent_id BIGINT NULL REFERENCES agents(agent_id),
    principal_id BIGINT NOT NULL,
    result VARCHAR(24) NOT NULL,
    occurred_at BIGINT NOT NULL,
    CONSTRAINT chk_agent_cli_account_switch_audit_result
        CHECK (result IN ('pending_onboarding', 'completed', 'expired', 'revoked', 'rejected'))
);
CREATE INDEX idx_agent_cli_account_switch_audit_principal
    ON agent_cli_account_switch_audit(principal_id, occurred_at DESC);

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

DROP INDEX IF EXISTS idx_agent_cli_account_switch_audit_principal;
DROP TABLE IF EXISTS agent_cli_account_switch_audit;
DROP INDEX IF EXISTS idx_agent_cli_account_switches_expiry;
DROP INDEX IF EXISTS idx_agent_cli_account_switches_target_pending;
DROP INDEX IF EXISTS idx_agent_cli_account_switches_principal_pending;
DROP TABLE IF EXISTS agent_cli_account_switches;
