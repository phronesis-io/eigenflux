-- +goose Up

-- System notification definitions (broadcast notification source)
CREATE TABLE system_notifications (
  notification_id BIGINT PRIMARY KEY,
  type VARCHAR(64) NOT NULL,
  content TEXT NOT NULL,
  status SMALLINT NOT NULL DEFAULT 0,
  audience_type VARCHAR(32) NOT NULL DEFAULT 'broadcast',
  audience_expression TEXT NOT NULL DEFAULT '',
  start_at BIGINT NOT NULL DEFAULT 0,
  end_at BIGINT NOT NULL DEFAULT 0,
  offline_at BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  CONSTRAINT chk_system_notifications_status CHECK (status IN (0, 1, 2))
);

-- Query active notifications: status=1, within lifecycle window
CREATE INDEX idx_system_notifications_active
  ON system_notifications(status, start_at, end_at);

-- Unified delivery tracking for all notification sources
CREATE TABLE notification_deliveries (
  delivery_id BIGSERIAL PRIMARY KEY,
  source_type VARCHAR(32) NOT NULL,
  source_id BIGINT NOT NULL,
  agent_id BIGINT NOT NULL,
  delivered_at BIGINT NOT NULL,
  CONSTRAINT uniq_notification_delivery UNIQUE (source_type, source_id, agent_id),
  CONSTRAINT fk_notification_deliveries_agent FOREIGN KEY (agent_id) REFERENCES agents(agent_id) ON DELETE CASCADE
);

-- Agent-centric query: "what has this agent received?"
CREATE INDEX idx_notification_deliveries_agent
  ON notification_deliveries(agent_id, delivered_at DESC);

-- Source-centric query: "who received this notification?"
CREATE INDEX idx_notification_deliveries_source
  ON notification_deliveries(source_type, source_id);

-- +goose Down

DROP TABLE IF EXISTS notification_deliveries;
DROP TABLE IF EXISTS system_notifications;
