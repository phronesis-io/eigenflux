-- +goose Up

CREATE TABLE notification_inbox (
    notification_id BIGINT PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    source_type VARCHAR(32) NOT NULL,
    source_id BIGINT NOT NULL,
    dedupe_key VARCHAR(192) NOT NULL UNIQUE,
    event_kind VARCHAR(64) NOT NULL,
    order_id BIGINT NOT NULL,
    order_version BIGINT NOT NULL,
    payload_json JSONB NOT NULL,
    occurred_at BIGINT NOT NULL,
    received_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    acknowledged_at BIGINT,
    last_delivery_error TEXT NOT NULL DEFAULT '',
    CONSTRAINT chk_notification_inbox_source CHECK (source_type = 'commission_order'),
    CONSTRAINT chk_notification_inbox_ids CHECK (
        notification_id > 0 AND agent_id > 0 AND source_id > 0 AND order_id > 0 AND order_version > 0
    ),
    CONSTRAINT chk_notification_inbox_times CHECK (
        occurred_at > 0 AND received_at > 0 AND expires_at > received_at
    ),
    CONSTRAINT uniq_notification_inbox_source UNIQUE (source_type, source_id, agent_id)
);

CREATE INDEX idx_notification_inbox_pending
    ON notification_inbox(agent_id, occurred_at, order_version, notification_id)
    WHERE acknowledged_at IS NULL;
CREATE INDEX idx_notification_inbox_pending_retention
    ON notification_inbox(expires_at, notification_id)
    WHERE acknowledged_at IS NULL;
CREATE INDEX idx_notification_inbox_ack_retention
    ON notification_inbox(acknowledged_at, notification_id)
    WHERE acknowledged_at IS NOT NULL;
CREATE INDEX idx_notification_inbox_reconcile
    ON notification_inbox(dedupe_key, source_id);

-- +goose Down

DROP TABLE IF EXISTS notification_inbox;
