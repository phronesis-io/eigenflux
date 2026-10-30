-- +goose Up
CREATE TABLE integration_contexts (
    agent_id BIGINT PRIMARY KEY REFERENCES integration_agents(agent_id),
    source_revision BIGINT NOT NULL CHECK (source_revision > 0),
    generation BIGINT NOT NULL CHECK (generation > 0),
    content_hash VARCHAR(64) NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','processing','ready','failed')),
    claim_token TEXT NOT NULL DEFAULT '',
    lease_until BIGINT NOT NULL DEFAULT 0,
    retry_at BIGINT NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    profile_version BIGINT NOT NULL DEFAULT 0,
    applied_matching_version BIGINT NOT NULL DEFAULT 0,
    failure_code TEXT NOT NULL DEFAULT '',
    accepted_at BIGINT NOT NULL,
    processed_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX integration_contexts_pending ON integration_contexts(status,retry_at,lease_until);

CREATE TABLE integration_matching_profiles (
    agent_id BIGINT PRIMARY KEY REFERENCES integration_agents(agent_id),
    source_revision BIGINT NOT NULL,
    profile_version BIGINT NOT NULL,
    content_hash VARCHAR(64) NOT NULL,
    keywords JSONB NOT NULL,
    embedding BYTEA NOT NULL,
    embedding_model TEXT NOT NULL,
    updated_at BIGINT NOT NULL
);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM integration_contexts) THEN
        RAISE EXCEPTION 'integration context data exists; preserve it before rollback';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE integration_matching_profiles;
DROP TABLE integration_contexts;
