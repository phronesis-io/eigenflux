#!/usr/bin/env bash
# Run only in a disposable migrated database as its test administrator.
set -euo pipefail
: "${ACCOUNT_ERASURE_TEST_DSN:?set the isolated PostgreSQL test database DSN}"
psql "$ACCOUNT_ERASURE_TEST_DSN" -X -v ON_ERROR_STOP=1 <<'SQL'
BEGIN;
CREATE ROLE ef_erasure_test_app;
CREATE ROLE ef_erasure_test_operator;
SELECT 'CREATE ROLE eigenflux_account_eraser NOLOGIN' WHERE to_regrole('eigenflux_account_eraser') IS NULL \gexec
GRANT eigenflux_account_eraser TO ef_erasure_test_operator;
GRANT SELECT, INSERT, UPDATE ON agents TO ef_erasure_test_app, ef_erasure_test_operator;
GRANT SELECT, INSERT, UPDATE ON account_soft_delete_audit TO ef_erasure_test_app, ef_erasure_test_operator;
INSERT INTO agents (agent_id, email, agent_name, created_at, updated_at)
VALUES (-101, 'account-test@example.invalid', 'account-test', 1, 1);
GRANT eigenflux_account_eraser TO ef_erasure_test_app WITH ADMIN TRUE, INHERIT FALSE, SET FALSE;
SET SESSION AUTHORIZATION ef_erasure_test_app;
DO $$
BEGIN
    IF eigenflux_can_soft_delete_account() THEN
        RAISE EXCEPTION 'ordinary application role passed permission check';
    END IF;
    BEGIN
        UPDATE agents SET identity_state = 'deleted' WHERE agent_id = -101;
        RAISE EXCEPTION 'ordinary application role changed protected account state';
    EXCEPTION WHEN insufficient_privilege THEN NULL;
    END;
END $$;
RESET SESSION AUTHORIZATION;
SET SESSION AUTHORIZATION ef_erasure_test_operator;
INSERT INTO account_soft_delete_audit (agent_id, email_digest, operator_name, ticket)
VALUES (-101, decode(repeat('00',32),'hex'), 'wrong-operator', 'TEST');
UPDATE agents SET identity_state = 'deleted' WHERE agent_id = -101;
DO $$
BEGIN
    IF (SELECT operator_name FROM account_soft_delete_audit WHERE agent_id=-101) <> session_user THEN
        RAISE EXCEPTION 'audit operator attribution failed';
    END IF;
    BEGIN
        UPDATE agents SET identity_state='active' WHERE agent_id=-101;
        RAISE EXCEPTION 'operator restored a terminal identity';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        UPDATE account_soft_delete_audit SET ticket = 'CHANGED' WHERE agent_id = -101;
        RAISE EXCEPTION 'operator modified audit history';
    EXCEPTION WHEN insufficient_privilege THEN NULL;
    END;
END $$;
RESET SESSION AUTHORIZATION;
ROLLBACK;
SQL
