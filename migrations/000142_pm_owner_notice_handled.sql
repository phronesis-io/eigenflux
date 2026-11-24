-- +goose Up
ALTER TABLE pm_owner_notices
    ADD COLUMN handled_at BIGINT,
    ADD COLUMN handled_by TEXT,
    ADD CONSTRAINT pm_owner_notices_handled_state CHECK (
        (handled_at IS NULL AND handled_by IS NULL)
        OR (handled_at IS NOT NULL AND handled_at > 0
            AND handled_by IS NOT NULL AND handled_by IN ('console', 'agent'))
    );
CREATE INDEX idx_pm_owner_notices_handled
    ON pm_owner_notices (agent_id, handled_at DESC, id DESC)
    WHERE handled_at IS NOT NULL;

-- +goose Down
DROP INDEX idx_pm_owner_notices_handled;
ALTER TABLE pm_owner_notices
    DROP CONSTRAINT pm_owner_notices_handled_state,
    DROP COLUMN handled_by,
    DROP COLUMN handled_at;
