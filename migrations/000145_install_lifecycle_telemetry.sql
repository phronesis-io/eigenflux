-- +goose Up
CREATE TABLE install_lifecycle_sessions (
    install_session_id VARCHAR(32) PRIMARY KEY CHECK (install_session_id ~ '^[a-f0-9]{32}$'),
    proof_hash VARCHAR(64) NOT NULL,
    agent_id BIGINT REFERENCES agents(agent_id) ON DELETE CASCADE,
    bound_at BIGINT,
    event_count INTEGER NOT NULL DEFAULT 0 CHECK (event_count BETWEEN 0 AND 512),
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    CHECK ((agent_id IS NULL) = (bound_at IS NULL))
);
CREATE INDEX idx_install_lifecycle_sessions_agent ON install_lifecycle_sessions(agent_id, created_at);
CREATE INDEX idx_install_lifecycle_sessions_expires ON install_lifecycle_sessions(expires_at);

CREATE TABLE install_lifecycle_events (
    event_key VARCHAR(64) PRIMARY KEY,
    event_id VARCHAR(64) NOT NULL,
    install_session_id VARCHAR(32) REFERENCES install_lifecycle_sessions(install_session_id) ON DELETE CASCADE,
    agent_id BIGINT REFERENCES agents(agent_id) ON DELETE CASCADE,
    event_type VARCHAR(64) NOT NULL,
    result VARCHAR(16) NOT NULL,
    evidence VARCHAR(20) NOT NULL CHECK (evidence IN ('client_reported', 'server_observed')),
    properties JSONB NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(properties) = 'object'),
    payload_hash VARCHAR(64) NOT NULL,
    event_at BIGINT NOT NULL,
    received_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    CHECK (install_session_id IS NOT NULL OR (evidence = 'server_observed' AND agent_id IS NOT NULL))
);
CREATE INDEX idx_install_lifecycle_events_session ON install_lifecycle_events(install_session_id, event_at);
CREATE INDEX idx_install_lifecycle_events_agent ON install_lifecycle_events(agent_id, event_at);
CREATE INDEX idx_install_lifecycle_events_type ON install_lifecycle_events(event_type, event_at);
CREATE INDEX idx_install_lifecycle_events_expires ON install_lifecycle_events(expires_at);

-- Shared quotas prevent new-session floods across gateway replicas. Keys contain
-- only a fixed category, authenticated Agent ID or keyed IP digest, never a raw
-- IP address or proof.
CREATE TABLE install_lifecycle_limits (
    rate_key VARCHAR(96) NOT NULL,
    window_at BIGINT NOT NULL,
    used INTEGER NOT NULL,
    PRIMARY KEY (rate_key, window_at)
);
CREATE INDEX idx_install_lifecycle_limits_window ON install_lifecycle_limits(window_at);

-- Dashboard readers must never receive the installation proof verifier.
CREATE VIEW grafana_install_lifecycle_sessions AS
    SELECT install_session_id,agent_id,bound_at,event_count,created_at,expires_at
    FROM install_lifecycle_sessions;
CREATE VIEW grafana_install_lifecycle_events AS
    SELECT event_key,event_id,install_session_id,agent_id,event_type,result,evidence,
           properties,event_at,received_at,expires_at
    FROM install_lifecycle_events;
CREATE VIEW grafana_install_browser_events AS
    SELECT event_id,agent_id,
           encode(sha256(convert_to(console_session_id,'UTF8')),'hex') AS journey_id,
           event_type,'client_reported'::text AS evidence,event_at,created_at AS received_at
    FROM telemetry_events_v2
    WHERE event_type='dashboard_first_render' AND console_session_id IS NOT NULL;

-- +goose Down
DROP VIEW grafana_install_browser_events;
DROP VIEW grafana_install_lifecycle_events;
DROP VIEW grafana_install_lifecycle_sessions;
DROP TABLE install_lifecycle_limits;
DROP TABLE install_lifecycle_events;
DROP TABLE install_lifecycle_sessions;
