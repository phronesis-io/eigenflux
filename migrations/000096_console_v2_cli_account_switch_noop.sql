-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE agent_cli_account_switches DROP CONSTRAINT chk_agent_cli_account_switches_status;
ALTER TABLE agent_cli_account_switches DROP CONSTRAINT chk_agent_cli_account_switches_target;
ALTER TABLE agent_cli_account_switches DROP CONSTRAINT chk_agent_cli_account_switches_completion;
ALTER TABLE agent_cli_account_switch_audit DROP CONSTRAINT chk_agent_cli_account_switch_audit_result;

ALTER TABLE agent_cli_account_switches
    ADD CONSTRAINT chk_agent_cli_account_switches_status
        CHECK (status IN ('pending_target', 'pending_onboarding', 'completed', 'completed_noop', 'expired', 'revoked')),
    ADD CONSTRAINT chk_agent_cli_account_switches_target
        CHECK ((status IN ('pending_target', 'completed_noop') AND target_agent_id IS NULL AND target_console_session_id IS NULL)
            OR (status IN ('pending_onboarding', 'completed') AND target_agent_id IS NOT NULL AND target_console_session_id IS NOT NULL)
            OR status IN ('expired', 'revoked')),
    ADD CONSTRAINT chk_agent_cli_account_switches_completion
        CHECK ((status IN ('completed', 'completed_noop') AND completed_at IS NOT NULL)
            OR (status NOT IN ('completed', 'completed_noop') AND completed_at IS NULL));

ALTER TABLE agent_cli_account_switch_audit
    ADD CONSTRAINT chk_agent_cli_account_switch_audit_result
        CHECK (result IN ('pending_onboarding', 'completed', 'completed_noop', 'expired', 'revoked', 'rejected'));

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_cli_account_switches WHERE status = 'completed_noop')
        OR EXISTS (SELECT 1 FROM agent_cli_account_switch_audit WHERE result = 'completed_noop') THEN
        RAISE EXCEPTION 'cannot downgrade while completed_noop account-switch history exists';
    END IF;
END $$;

ALTER TABLE agent_cli_account_switches DROP CONSTRAINT chk_agent_cli_account_switches_status;
ALTER TABLE agent_cli_account_switches DROP CONSTRAINT chk_agent_cli_account_switches_target;
ALTER TABLE agent_cli_account_switches DROP CONSTRAINT chk_agent_cli_account_switches_completion;
ALTER TABLE agent_cli_account_switch_audit DROP CONSTRAINT chk_agent_cli_account_switch_audit_result;

ALTER TABLE agent_cli_account_switches
    ADD CONSTRAINT chk_agent_cli_account_switches_status
        CHECK (status IN ('pending_target', 'pending_onboarding', 'completed', 'expired', 'revoked')),
    ADD CONSTRAINT chk_agent_cli_account_switches_target
        CHECK ((status = 'pending_target' AND target_agent_id IS NULL AND target_console_session_id IS NULL)
            OR (status IN ('pending_onboarding', 'completed') AND target_agent_id IS NOT NULL AND target_console_session_id IS NOT NULL)
            OR status IN ('expired', 'revoked')),
    ADD CONSTRAINT chk_agent_cli_account_switches_completion
        CHECK ((status = 'completed' AND completed_at IS NOT NULL)
            OR (status <> 'completed' AND completed_at IS NULL));

ALTER TABLE agent_cli_account_switch_audit
    ADD CONSTRAINT chk_agent_cli_account_switch_audit_result
        CHECK (result IN ('pending_onboarding', 'completed', 'expired', 'revoked', 'rejected'));
