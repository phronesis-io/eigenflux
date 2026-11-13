-- +goose Up
CREATE TABLE uninstall_feedback (
    attempt_id VARCHAR(32) PRIMARY KEY,
    agent_id BIGINT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('manual', 'agent')),
    reason_category TEXT NOT NULL CHECK (reason_category IN (
        'declined', 'other', 'usability', 'cost', 'performance', 'privacy', 'no_longer_needed'
    )),
    reason TEXT,
    created_at BIGINT NOT NULL,
    CHECK ((reason_category = 'declined' AND reason IS NULL)
        OR (reason_category <> 'declined' AND reason IS NOT NULL AND octet_length(reason) BETWEEN 1 AND 1000))
);

REVOKE ALL ON uninstall_feedback FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ops_readonly') THEN
        REVOKE ALL ON uninstall_feedback FROM ops_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro_v2') THEN
        REVOKE ALL ON uninstall_feedback FROM grafana_ro_v2;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE uninstall_feedback;
