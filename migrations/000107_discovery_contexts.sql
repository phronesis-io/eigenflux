-- +goose Up
-- +goose StatementBegin
CREATE TABLE discovery_contexts (
 context_id BIGINT PRIMARY KEY,
 agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
 persistence TEXT NOT NULL CHECK (persistence IN ('saved','ephemeral')),
 input_origin TEXT NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('active','paused','completed','expired')),
 priority DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 1),
 source_kind TEXT NOT NULL DEFAULT '',
 revision BIGINT NOT NULL CHECK (revision > 0),
 compiled JSONB NOT NULL,
 embedding BYTEA,
 deadline_ms BIGINT,
 expires_at BIGINT NOT NULL DEFAULT 0,
 created_at BIGINT NOT NULL,
 updated_at BIGINT NOT NULL,
 idempotency_key TEXT NOT NULL DEFAULT '',
 idempotency_hash TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_discovery_contexts_active ON discovery_contexts(agent_id, priority DESC, updated_at DESC, context_id) WHERE persistence='saved' AND state='active';
CREATE INDEX idx_discovery_contexts_owner_updated ON discovery_contexts(agent_id, updated_at DESC, context_id);
CREATE INDEX idx_discovery_contexts_expiry ON discovery_contexts(expires_at) WHERE persistence='ephemeral';
CREATE UNIQUE INDEX uq_discovery_contexts_idempotency ON discovery_contexts(agent_id,idempotency_key) WHERE idempotency_key <> '';
ALTER TABLE processed_items ADD COLUMN retrieval_slots JSONB NOT NULL DEFAULT '{}'::jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE processed_items DROP COLUMN retrieval_slots;
DROP TABLE discovery_contexts;
-- +goose StatementEnd
