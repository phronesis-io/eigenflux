-- +goose NO TRANSACTION
-- +goose Up

ALTER TABLE agent_commands
    ADD COLUMN IF NOT EXISTS action_idempotency_key VARCHAR(128) NULL;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_agent_commands_attention_action_key
    ON agent_commands(agent_id, action_idempotency_key)
    WHERE command_type = 'attention_action' AND action_idempotency_key IS NOT NULL;


-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS uq_agent_commands_attention_action_key;
ALTER TABLE agent_commands DROP COLUMN IF EXISTS action_idempotency_key;
