-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE agent_credential_sessions
    ADD COLUMN rotation_request_id VARCHAR(128) NULL,
    ADD COLUMN rotation_request_hash VARCHAR(128) NULL,
    ADD CONSTRAINT chk_agent_credential_rotation_receipt CHECK (
        (rotation_request_id IS NULL) = (rotation_request_hash IS NULL)
    );
CREATE UNIQUE INDEX uq_agent_credential_rotation_request
    ON agent_credential_sessions(principal_id, rotation_request_id)
    WHERE rotation_request_id IS NOT NULL;

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_principals LIMIT 1) THEN
        RAISE EXCEPTION 'unsafe Console V2 schema rollback: disable feature flags instead';
    END IF;
END $$;
-- +goose StatementEnd

DROP INDEX IF EXISTS uq_agent_credential_rotation_request;
ALTER TABLE agent_credential_sessions
    DROP CONSTRAINT IF EXISTS chk_agent_credential_rotation_receipt,
    DROP COLUMN IF EXISTS rotation_request_hash,
    DROP COLUMN IF EXISTS rotation_request_id;
