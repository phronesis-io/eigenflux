-- +goose Up
CREATE TABLE agent_install_receipts (
    agent_id BIGINT PRIMARY KEY REFERENCES agents(agent_id) ON DELETE CASCADE,
    runtime_name TEXT NOT NULL DEFAULT '',
    receipt_supported BOOLEAN NOT NULL,
    acknowledged_at BIGINT CHECK (acknowledged_at IS NULL OR (acknowledged_at > 0 AND receipt_supported))
);

-- +goose Down
DROP TABLE agent_install_receipts;
