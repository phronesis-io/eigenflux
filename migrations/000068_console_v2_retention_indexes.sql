-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- Global cleanup workers need time-first indexes. Request paths keep their
-- existing Agent-first indexes; these additions prevent retention sweeps from
-- scanning whole V2 tables as data grows.
CREATE INDEX idx_agent_runtime_leases_expiry
    ON agent_runtime_leases(lease_until, agent_id, runtime_instance_id);
CREATE INDEX idx_control_wakeup_retention
    ON control_wakeup_outbox(status, created_at, outbox_id);
CREATE INDEX idx_feed_batches_retention
    ON feed_batches(status, created_at, batch_id);
CREATE INDEX idx_agent_commands_retention
    ON agent_commands(status, completed_at, command_id);
CREATE INDEX idx_agent_attention_retention
    ON agent_attention_items(status, created_at, attention_id);
CREATE INDEX idx_agent_credential_sessions_retention
    ON agent_credential_sessions(absolute_expires_at, session_id);
CREATE INDEX idx_console_v2_sessions_retention
    ON console_v2_sessions(absolute_expires_at, session_id);
CREATE INDEX idx_feed_batch_items_source_lookup
    ON feed_batch_items(source_type, source_id, batch_id);

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP INDEX IF EXISTS idx_console_v2_sessions_retention;
DROP INDEX IF EXISTS idx_feed_batch_items_source_lookup;
DROP INDEX IF EXISTS idx_agent_credential_sessions_retention;
DROP INDEX IF EXISTS idx_agent_attention_retention;
DROP INDEX IF EXISTS idx_agent_commands_retention;
DROP INDEX IF EXISTS idx_feed_batches_retention;
DROP INDEX IF EXISTS idx_control_wakeup_retention;
DROP INDEX IF EXISTS idx_agent_runtime_leases_expiry;
