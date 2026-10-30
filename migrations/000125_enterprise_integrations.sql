-- +goose Up
CREATE TABLE integration_control (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    revision BIGINT NOT NULL DEFAULT 1,
    actor TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    updated_at BIGINT NOT NULL
);
INSERT INTO integration_control(id, actor, updated_at)
VALUES (1, 'migration', (extract(epoch FROM clock_timestamp()) * 1000)::bigint);

CREATE TABLE integration_apps (
    app_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    credential_hash VARCHAR(64) NOT NULL UNIQUE,
    max_agents INTEGER NOT NULL CHECK (max_agents > 0),
    max_installations_per_agent INTEGER NOT NULL DEFAULT 1 CHECK (max_installations_per_agent > 0),
    min_poll_interval_seconds INTEGER NOT NULL DEFAULT 3600 CHECK (min_poll_interval_seconds BETWEEN 10 AND 86400),
    revision BIGINT NOT NULL DEFAULT 1,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);

CREATE TABLE integration_agents (
    agent_id BIGINT PRIMARY KEY REFERENCES agents(agent_id),
    app_id TEXT NOT NULL REFERENCES integration_apps(app_id),
    external_agent_id TEXT NOT NULL CHECK (length(external_agent_id) BETWEEN 1 AND 128),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    requested_poll_interval_seconds INTEGER NOT NULL DEFAULT 3600 CHECK (requested_poll_interval_seconds BETWEEN 10 AND 86400),
    next_poll_at BIGINT NOT NULL DEFAULT 0,
    runtime_name VARCHAR(64) NOT NULL,
    runtime_version VARCHAR(64) NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    UNIQUE(app_id, external_agent_id)
);

CREATE TABLE integration_installations (
    installation_id TEXT PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES integration_agents(agent_id),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    sdk_version VARCHAR(32) NOT NULL,
    runtime_instance_id TEXT NOT NULL,
    last_seen_at BIGINT NOT NULL,
    created_at BIGINT NOT NULL
);
CREATE INDEX integration_installations_agent ON integration_installations(agent_id);

CREATE TABLE integration_audit (
    id BIGSERIAL PRIMARY KEY,
    app_id TEXT,
    agent_id BIGINT,
    actor TEXT NOT NULL,
    operation TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    occurred_at BIGINT NOT NULL
);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM integration_agents) THEN
        RAISE EXCEPTION 'managed identities exist; preserve their application bindings before rollback';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE integration_audit;
DROP TABLE integration_installations;
DROP TABLE integration_agents;
DROP TABLE integration_apps;
DROP TABLE integration_control;
