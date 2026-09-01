-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE agent_bootstrap_grants
    ADD COLUMN subject_agent_id BIGINT NULL REFERENCES agents(agent_id) ON DELETE CASCADE;

CREATE INDEX idx_agent_bootstrap_grants_subject
    ON agent_bootstrap_grants(subject_agent_id, status, expires_at)
    WHERE subject_agent_id IS NOT NULL;

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_bootstrap_grants WHERE subject_agent_id IS NOT NULL LIMIT 1) THEN
        RAISE EXCEPTION 'unsafe legacy Agent upgrade rollback: subject-bound grants preserve existing identities';
    END IF;
END $$;
-- +goose StatementEnd

DROP INDEX IF EXISTS idx_agent_bootstrap_grants_subject;
ALTER TABLE agent_bootstrap_grants DROP COLUMN IF EXISTS subject_agent_id;
