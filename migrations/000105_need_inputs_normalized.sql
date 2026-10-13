-- +goose Up
-- +goose StatementBegin
CREATE TABLE need_inputs (
    need_input_id BIGINT PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    intent_id BIGINT NOT NULL,
    intent_version BIGINT NOT NULL CHECK (intent_version > 0),
    schema_version TEXT NOT NULL CHECK (schema_version = 'need_input.v1'),
    input JSONB NOT NULL CHECK (jsonb_typeof(input) = 'object'),
    intent_snapshot JSONB NOT NULL CHECK (jsonb_typeof(intent_snapshot) = 'object'),
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'normalized', 'failed', 'superseded')),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 128),
    request_hash TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    CONSTRAINT fk_need_inputs_intent
        FOREIGN KEY (agent_id, intent_id)
        REFERENCES agent_intent_actions(agent_id, intent_id)
        ON DELETE CASCADE,
    CONSTRAINT uq_need_inputs_owner_key UNIQUE (agent_id, idempotency_key),
    CONSTRAINT uq_need_inputs_owner_id UNIQUE (agent_id, need_input_id, intent_id, intent_version),
    CONSTRAINT chk_need_inputs_link CHECK ((input->>'schema_version' = schema_version
        AND input->>'intent_id' = intent_id::text
        AND input->>'intent_version' = intent_version::text
        AND input->>'need_type' IN ('broadcast', 'agent', 'commission')) IS TRUE)
);
CREATE INDEX idx_need_inputs_intent_version
    ON need_inputs(agent_id, intent_id, intent_version, need_input_id DESC);
CREATE INDEX idx_need_inputs_status
    ON need_inputs(status, updated_at DESC);

CREATE TABLE normalized_needs (
    normalized_need_id BIGINT PRIMARY KEY,
    need_input_id BIGINT NOT NULL,
    agent_id BIGINT NOT NULL,
    intent_id BIGINT NOT NULL,
    intent_version BIGINT NOT NULL CHECK (intent_version > 0),
    schema_version TEXT NOT NULL CHECK (schema_version = 'normalized_need.v1'),
    normalized JSONB NOT NULL CHECK (jsonb_typeof(normalized) = 'object'),
    normalizer_version TEXT NOT NULL CHECK (length(trim(normalizer_version)) > 0),
    taxonomy_version TEXT NOT NULL DEFAULT '',
    mapping_status TEXT NOT NULL DEFAULT 'unmapped'
        CHECK (mapping_status IN ('unmapped', 'partial', 'mapped')),
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'superseded', 'rejected')),
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    CONSTRAINT fk_normalized_needs_input
        FOREIGN KEY (agent_id, need_input_id, intent_id, intent_version)
        REFERENCES need_inputs(agent_id, need_input_id, intent_id, intent_version)
        ON DELETE CASCADE,
    CONSTRAINT uq_normalized_needs_revision
        UNIQUE (need_input_id, normalizer_version, taxonomy_version),
    CONSTRAINT chk_normalized_needs_payload CHECK ((jsonb_typeof(normalized->'desc') = 'string'
        AND jsonb_typeof(normalized->'candidate_needs') = 'array') IS TRUE)
);
CREATE UNIQUE INDEX uq_normalized_needs_active_input
    ON normalized_needs(need_input_id) WHERE status = 'active';
CREATE INDEX idx_normalized_needs_active
    ON normalized_needs(agent_id, status, updated_at DESC);
CREATE INDEX idx_normalized_needs_intent
    ON normalized_needs(agent_id, intent_id, intent_version, status);
CREATE INDEX idx_normalized_needs_mapping
    ON normalized_needs(mapping_status, normalized_need_id) WHERE status = 'active';
-- Eligibility follows the current human-managed intent, without rewriting history.
-- Vocabulary coverage and offline processing never gate eligibility.
CREATE VIEW current_normalized_needs AS
SELECT n.* FROM normalized_needs n
JOIN need_inputs i ON i.need_input_id = n.need_input_id
JOIN agent_intent_actions a ON a.agent_id = n.agent_id AND a.intent_id = n.intent_id
WHERE n.status = 'active' AND i.status = 'normalized'
    AND a.status = 'active' AND a.version = n.intent_version;
-- +goose StatementEnd

-- +goose Down
DROP VIEW current_normalized_needs;
DROP TABLE normalized_needs;
DROP TABLE need_inputs;
