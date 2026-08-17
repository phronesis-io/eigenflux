-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- Keep every attention/intent reference inside the same Agent boundary. The
-- redundant agent_id lets PostgreSQL enforce ownership without application
-- joins and keeps list reads index-friendly.
ALTER TABLE agent_attention_intents
    ADD COLUMN agent_id BIGINT NULL REFERENCES agents(agent_id) ON DELETE CASCADE;
UPDATE agent_attention_intents link
SET agent_id = item.agent_id
FROM agent_attention_items item
WHERE item.attention_id = link.attention_id;
ALTER TABLE agent_attention_intents
    ALTER COLUMN agent_id SET NOT NULL,
    DROP CONSTRAINT agent_attention_intents_pkey,
    DROP CONSTRAINT agent_attention_intents_attention_id_fkey,
    DROP CONSTRAINT agent_attention_intents_intent_id_fkey,
    ADD CONSTRAINT agent_attention_intents_pkey PRIMARY KEY (agent_id, attention_id, intent_id),
    ADD CONSTRAINT fk_agent_attention_intents_attention
        FOREIGN KEY (agent_id, attention_id)
        REFERENCES agent_attention_items(agent_id, attention_id) ON DELETE CASCADE,
    ADD CONSTRAINT fk_agent_attention_intents_intent
        FOREIGN KEY (agent_id, intent_id)
        REFERENCES agent_intent_actions(agent_id, intent_id) ON DELETE CASCADE;

-- A source can create another Attention item only after the previous one has
-- reached a terminal state. This makes Feed redelivery cheap and idempotent.
CREATE UNIQUE INDEX uq_agent_attention_open_source
    ON agent_attention_items(agent_id, source_type, source_id)
    WHERE status = 'open';

CREATE TABLE agent_runtime_leases (
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    runtime_instance_id VARCHAR(128) NOT NULL,
    capabilities TEXT[] NOT NULL DEFAULT '{}',
    session_ref TEXT NULL,
    context_revision_applied BIGINT NULL,
    lease_until BIGINT NOT NULL,
    last_heartbeat_at BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (agent_id, runtime_instance_id),
    CONSTRAINT fk_agent_runtime_context
        FOREIGN KEY (agent_id, context_revision_applied)
        REFERENCES agent_context_revisions(agent_id, revision),
    CONSTRAINT chk_agent_runtime_context_revision
        CHECK (context_revision_applied IS NULL OR context_revision_applied > 0),
    CONSTRAINT chk_agent_runtime_capabilities CHECK (cardinality(capabilities) <= 32),
    CONSTRAINT chk_agent_runtime_session_ref CHECK (session_ref IS NULL OR length(session_ref) <= 512)
);
CREATE INDEX idx_agent_runtime_leases_active
    ON agent_runtime_leases(agent_id, lease_until DESC);

-- The database is the durable fact source. A small dispatcher may publish
-- pending rows to Redis, but command creation never depends on Redis being up.
CREATE TABLE control_wakeup_outbox (
    outbox_id BIGSERIAL PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    event_type VARCHAR(32) NOT NULL,
    entity_id BIGINT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    attempt_count INT NOT NULL DEFAULT 0,
    next_attempt_at BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    delivered_at BIGINT NULL,
    last_error TEXT NULL,
    CONSTRAINT uq_control_wakeup_entity UNIQUE (event_type, entity_id),
    CONSTRAINT chk_control_wakeup_payload CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT chk_control_wakeup_status CHECK (status IN ('pending', 'delivered', 'dead')),
    CONSTRAINT chk_control_wakeup_attempts CHECK (attempt_count >= 0)
);
CREATE INDEX idx_control_wakeup_pending
    ON control_wakeup_outbox(next_attempt_at, outbox_id)
    WHERE status = 'pending';

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

DROP TABLE IF EXISTS control_wakeup_outbox;
DROP TABLE IF EXISTS agent_runtime_leases;
DROP INDEX IF EXISTS uq_agent_attention_open_source;

ALTER TABLE agent_attention_intents
    DROP CONSTRAINT fk_agent_attention_intents_intent,
    DROP CONSTRAINT fk_agent_attention_intents_attention,
    DROP CONSTRAINT agent_attention_intents_pkey,
    ADD CONSTRAINT agent_attention_intents_pkey PRIMARY KEY (attention_id, intent_id),
    ADD CONSTRAINT agent_attention_intents_attention_id_fkey
        FOREIGN KEY (attention_id) REFERENCES agent_attention_items(attention_id) ON DELETE CASCADE,
    ADD CONSTRAINT agent_attention_intents_intent_id_fkey
        FOREIGN KEY (intent_id) REFERENCES agent_intent_actions(intent_id) ON DELETE CASCADE,
    DROP COLUMN agent_id;
