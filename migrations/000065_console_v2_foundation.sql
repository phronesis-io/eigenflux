-- +goose NO TRANSACTION
-- +goose Up
SET lock_timeout = '5s';
SET statement_timeout = '5min';

ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS email_kind VARCHAR(24) NOT NULL DEFAULT 'legacy_real';

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'agents'::regclass AND conname = 'chk_agents_email_kind'
    ) THEN
        ALTER TABLE agents ADD CONSTRAINT chk_agents_email_kind
            CHECK (email_kind IN ('legacy_real', 'internal_alias', 'v2_bound')) NOT VALID;
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE agent_cards
    ADD COLUMN IF NOT EXISTS public_card_version BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS public_card_generated_at BIGINT NOT NULL DEFAULT 0;

ALTER TABLE agent_activity_log
    ADD COLUMN IF NOT EXISTS agent_seq BIGINT NULL,
    ADD COLUMN IF NOT EXISTS source_event_id VARCHAR(128) NULL;

-- Existing Card versions are copied by the bounded, resumable
-- console_v2_backfill job. Avoid a migration-time full-table rewrite.
-- The three existing-table ALTER statements above auto-commit independently;
-- this transaction contains only new V2 schema so V1 locks are not retained
-- while the rest of the foundation is built.
BEGIN;

CREATE TABLE IF NOT EXISTS agent_principals (
    principal_id BIGSERIAL PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    key_type VARCHAR(32) NOT NULL DEFAULT 'ed25519-v1',
    key_fingerprint VARCHAR(128) NOT NULL,
    public_key BYTEA NOT NULL,
    key_version BIGINT NOT NULL DEFAULT 1,
    status VARCHAR(24) NOT NULL DEFAULT 'limited',
    created_at BIGINT NOT NULL,
    last_seen_at BIGINT NOT NULL,
    revoked_at BIGINT NULL,
    CONSTRAINT uq_agent_principals_fingerprint UNIQUE (key_type, key_fingerprint),
    CONSTRAINT chk_agent_principals_key_type CHECK (key_type = 'ed25519-v1'),
    CONSTRAINT chk_agent_principals_public_key CHECK (octet_length(public_key) = 32),
    CONSTRAINT chk_agent_principals_status CHECK (status IN ('limited', 'active', 'suspended', 'revoked'))
);
CREATE INDEX IF NOT EXISTS idx_agent_principals_agent_status
    ON agent_principals(agent_id, status, principal_id);

CREATE TABLE IF NOT EXISTS agent_email_bindings (
    binding_id BIGSERIAL PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    normalized_email VARCHAR(255) NOT NULL,
    normalization_version SMALLINT NOT NULL DEFAULT 1,
    verification_state VARCHAR(32) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    verified_at BIGINT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    revoked_at BIGINT NULL,
    CONSTRAINT chk_agent_email_bindings_canonical
        CHECK (normalized_email = lower(btrim(normalized_email))),
    CONSTRAINT chk_agent_email_bindings_verification
        CHECK (verification_state IN ('legacy_unverified', 'verified', 'revoked')),
    CONSTRAINT chk_agent_email_bindings_status
        CHECK (status IN ('active', 'revoked'))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_email_binding_active_agent
    ON agent_email_bindings(agent_id)
    WHERE status = 'active';
CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_email_binding_active_verified
    ON agent_email_bindings(normalized_email)
    WHERE status = 'active' AND verification_state = 'verified';
CREATE INDEX IF NOT EXISTS idx_agent_email_bindings_email_status
    ON agent_email_bindings(normalized_email, status, binding_id);

CREATE TABLE IF NOT EXISTS agent_bootstrap_grants (
    jti_hash VARCHAR(128) PRIMARY KEY,
    key_fingerprint VARCHAR(128) NOT NULL,
    audience VARCHAR(64) NOT NULL,
    channel VARCHAR(64) NOT NULL,
    policy VARCHAR(64) NOT NULL,
    entitlement_hash VARCHAR(128) NOT NULL UNIQUE,
    request_id VARCHAR(128) NOT NULL UNIQUE,
    status VARCHAR(16) NOT NULL DEFAULT 'issued',
    expires_at BIGINT NOT NULL,
    consumed_at BIGINT NULL,
    consumed_by_agent_id BIGINT NULL REFERENCES agents(agent_id) ON DELETE SET NULL,
    created_at BIGINT NOT NULL,
    CONSTRAINT chk_agent_bootstrap_grants_audience CHECK (audience = 'agent_provision'),
    CONSTRAINT chk_agent_bootstrap_grants_status CHECK (status IN ('issued', 'consumed', 'revoked'))
);
CREATE INDEX IF NOT EXISTS idx_agent_bootstrap_grants_expiry
    ON agent_bootstrap_grants(expires_at, status);
CREATE INDEX IF NOT EXISTS idx_agent_bootstrap_grants_key_status
    ON agent_bootstrap_grants(key_fingerprint, status, expires_at);

CREATE TABLE IF NOT EXISTS agent_signature_nonces (
    nonce_hash VARCHAR(128) PRIMARY KEY,
    key_fingerprint VARCHAR(128) NOT NULL,
    domain VARCHAR(64) NOT NULL,
    expires_at BIGINT NOT NULL,
    consumed_at BIGINT NULL,
    created_at BIGINT NOT NULL,
    CONSTRAINT chk_agent_signature_nonces_domain
        CHECK (domain IN ('provision', 'refresh', 'add_device', 'handoff'))
);
CREATE INDEX IF NOT EXISTS idx_agent_signature_nonces_expiry
    ON agent_signature_nonces(expires_at, consumed_at);

CREATE TABLE IF NOT EXISTS agent_credential_sessions (
    session_id BIGSERIAL PRIMARY KEY,
    principal_id BIGINT NOT NULL REFERENCES agent_principals(principal_id) ON DELETE CASCADE,
    family_id VARCHAR(128) NOT NULL,
    access_token_hash VARCHAR(128) NOT NULL UNIQUE,
    refresh_token_hash VARCHAR(128) NOT NULL UNIQUE,
    audience VARCHAR(64) NOT NULL,
    scopes TEXT[] NOT NULL DEFAULT '{}'::text[],
    rotation_counter BIGINT NOT NULL DEFAULT 0,
    issued_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    absolute_expires_at BIGINT NOT NULL,
    replaced_by_session_id BIGINT NULL REFERENCES agent_credential_sessions(session_id) ON DELETE SET NULL,
    revoked_at BIGINT NULL,
    last_seen_at BIGINT NOT NULL,
    CONSTRAINT chk_agent_credential_sessions_expiry
        CHECK (issued_at < expires_at AND expires_at <= absolute_expires_at)
);
CREATE INDEX IF NOT EXISTS idx_agent_credential_sessions_principal_active
    ON agent_credential_sessions(principal_id, expires_at)
    WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_agent_credential_sessions_family_active
    ON agent_credential_sessions(family_id, rotation_counter DESC)
    WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS v2_email_challenges (
    challenge_id VARCHAR(64) PRIMARY KEY,
    purpose VARCHAR(24) NOT NULL,
    normalized_email_hash VARCHAR(128) NOT NULL,
    subject_agent_id BIGINT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    console_session_id VARCHAR(128) NULL,
    otp_hmac VARCHAR(128) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    attempt_count INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 5,
    expires_at BIGINT NOT NULL,
    consumed_at BIGINT NULL,
    client_ip_hash VARCHAR(128) NULL,
    created_at BIGINT NOT NULL,
    CONSTRAINT chk_v2_email_challenges_purpose
        CHECK (purpose IN ('bind', 'login', 'recovery', 'add_device')),
    CONSTRAINT chk_v2_email_challenges_status
        CHECK (status IN ('pending', 'consumed', 'expired', 'revoked')),
    CONSTRAINT chk_v2_email_challenges_attempts
        CHECK (attempt_count >= 0 AND max_attempts BETWEEN 1 AND 10)
);
CREATE INDEX IF NOT EXISTS idx_v2_email_challenges_expiry
    ON v2_email_challenges(expires_at, status);
CREATE INDEX IF NOT EXISTS idx_v2_email_challenges_email_purpose
    ON v2_email_challenges(normalized_email_hash, purpose, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_v2_email_challenges_ip_created
    ON v2_email_challenges(client_ip_hash, created_at DESC)
    WHERE client_ip_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_v2_email_challenges_session_created
    ON v2_email_challenges(console_session_id, created_at DESC)
    WHERE console_session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_v2_email_challenges_created
    ON v2_email_challenges(created_at DESC);

CREATE TABLE IF NOT EXISTS console_v2_handoffs (
    ticket_hash VARCHAR(128) PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    principal_id BIGINT NOT NULL REFERENCES agent_principals(principal_id) ON DELETE CASCADE,
    console_scope TEXT[] NOT NULL DEFAULT '{}'::text[],
    browser_nonce_hash VARCHAR(128) NULL,
    expires_at BIGINT NOT NULL,
    consumed_at BIGINT NULL,
    created_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_console_v2_handoffs_expiry
    ON console_v2_handoffs(expires_at, consumed_at);

CREATE TABLE IF NOT EXISTS console_v2_sessions (
    session_id VARCHAR(128) PRIMARY KEY,
    session_secret_hash VARCHAR(128) NOT NULL UNIQUE,
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    principal_id BIGINT NOT NULL REFERENCES agent_principals(principal_id) ON DELETE CASCADE,
    csrf_secret_hash VARCHAR(128) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    scopes TEXT[] NOT NULL DEFAULT '{}'::text[],
    issued_at BIGINT NOT NULL,
    idle_expires_at BIGINT NOT NULL,
    absolute_expires_at BIGINT NOT NULL,
    last_seen_at BIGINT NOT NULL,
    revoked_at BIGINT NULL,
    CONSTRAINT chk_console_v2_sessions_status CHECK (status IN ('active', 'revoked', 'expired')),
    CONSTRAINT chk_console_v2_sessions_expiry
        CHECK (issued_at < idle_expires_at AND idle_expires_at <= absolute_expires_at)
);
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'v2_email_challenges'::regclass
          AND conname = 'fk_v2_email_challenges_console_session'
    ) THEN
        ALTER TABLE v2_email_challenges
            ADD CONSTRAINT fk_v2_email_challenges_console_session
            FOREIGN KEY (console_session_id) REFERENCES console_v2_sessions(session_id) ON DELETE SET NULL;
    END IF;
END $$;
-- +goose StatementEnd
CREATE INDEX IF NOT EXISTS idx_console_v2_sessions_agent_active
    ON console_v2_sessions(agent_id, idle_expires_at)
    WHERE status = 'active';

CREATE TABLE IF NOT EXISTS agent_onboarding_v2 (
    agent_id BIGINT PRIMARY KEY REFERENCES agents(agent_id) ON DELETE CASCADE,
    state VARCHAR(24) NOT NULL DEFAULT 'in_progress',
    current_step SMALLINT NOT NULL DEFAULT 2,
    revision BIGINT NOT NULL DEFAULT 1,
    active_context_revision BIGINT NULL,
    completed_at BIGINT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    CONSTRAINT chk_agent_onboarding_v2_state
        CHECK (state IN ('in_progress', 'migration_pending', 'completed')),
    CONSTRAINT chk_agent_onboarding_v2_step CHECK (current_step BETWEEN 1 AND 5),
    CONSTRAINT chk_agent_onboarding_v2_completed CHECK (
        (state = 'completed' AND completed_at IS NOT NULL AND active_context_revision IS NOT NULL)
        OR (state <> 'completed' AND completed_at IS NULL)
    )
);

CREATE TABLE IF NOT EXISTS agent_onboarding_drafts (
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    revision BIGINT NOT NULL,
    draft_data JSONB NOT NULL DEFAULT '{}'::jsonb,
    field_provenance JSONB NOT NULL DEFAULT '{}'::jsonb,
    actor_type VARCHAR(24) NOT NULL,
    request_id VARCHAR(128) NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (agent_id, revision),
    CONSTRAINT uq_agent_onboarding_drafts_request UNIQUE (agent_id, request_id),
    CONSTRAINT chk_agent_onboarding_drafts_data CHECK (jsonb_typeof(draft_data) = 'object'),
    CONSTRAINT chk_agent_onboarding_drafts_provenance CHECK (jsonb_typeof(field_provenance) = 'object'),
    CONSTRAINT chk_agent_onboarding_drafts_actor
        CHECK (actor_type IN ('agent_prefill', 'human_edit', 'system_derived'))
);

CREATE TABLE IF NOT EXISTS agent_idempotency_requests (
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    operation VARCHAR(64) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    request_hash VARCHAR(128) NOT NULL,
    response_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    expires_at BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (agent_id, operation, idempotency_key),
    CONSTRAINT chk_agent_idempotency_response CHECK (jsonb_typeof(response_snapshot) = 'object')
);
CREATE INDEX IF NOT EXISTS idx_agent_idempotency_requests_expiry
    ON agent_idempotency_requests(expires_at);

CREATE TABLE IF NOT EXISTS agent_network_goals (
    goal_id BIGSERIAL PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    goal_text TEXT NOT NULL,
    source VARCHAR(24) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    version BIGINT NOT NULL DEFAULT 1,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    CONSTRAINT chk_agent_network_goals_source
        CHECK (source IN ('agent_prefill', 'human_edit', 'system_derived')),
    CONSTRAINT chk_agent_network_goals_status CHECK (status IN ('active', 'deleted'))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_network_goals_active
    ON agent_network_goals(agent_id)
    WHERE status = 'active';

CREATE TABLE IF NOT EXISTS agent_intent_actions (
    intent_id BIGSERIAL PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    watch_for TEXT NOT NULL,
    trigger_when TEXT NOT NULL,
    action_instruction TEXT NOT NULL,
    action_policy VARCHAR(24) NOT NULL,
    priority SMALLINT NOT NULL DEFAULT 0,
    source VARCHAR(24) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    version BIGINT NOT NULL DEFAULT 1,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    CONSTRAINT chk_agent_intent_actions_policy
        CHECK (action_policy IN ('analyze_only', 'draft', 'network_action', 'trade_action')),
    CONSTRAINT chk_agent_intent_actions_source
        CHECK (source IN ('agent_prefill', 'human_edit', 'system_derived')),
    CONSTRAINT chk_agent_intent_actions_status CHECK (status IN ('active', 'paused', 'deleted'))
);
CREATE INDEX IF NOT EXISTS idx_agent_intent_actions_agent_status
    ON agent_intent_actions(agent_id, status, priority DESC, intent_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_intent_actions_agent_intent
    ON agent_intent_actions(agent_id, intent_id);

CREATE TABLE IF NOT EXISTS agent_context_heads (
    agent_id BIGINT PRIMARY KEY REFERENCES agents(agent_id) ON DELETE CASCADE,
    current_revision BIGINT NOT NULL DEFAULT 0,
    active_revision BIGINT NULL,
    updated_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_context_revisions (
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    revision BIGINT NOT NULL,
    compiled_context JSONB NOT NULL,
    schema_version INT NOT NULL DEFAULT 1,
    generated_at BIGINT NOT NULL,
    PRIMARY KEY (agent_id, revision),
    CONSTRAINT chk_agent_context_revisions_context CHECK (jsonb_typeof(compiled_context) = 'object')
);

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'agent_context_heads'::regclass
          AND conname = 'fk_agent_context_heads_active_revision'
    ) THEN
        ALTER TABLE agent_context_heads
            ADD CONSTRAINT fk_agent_context_heads_active_revision
            FOREIGN KEY (agent_id, active_revision)
            REFERENCES agent_context_revisions(agent_id, revision)
            DEFERRABLE INITIALLY DEFERRED;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'agent_onboarding_v2'::regclass
          AND conname = 'fk_agent_onboarding_active_context'
    ) THEN
        ALTER TABLE agent_onboarding_v2
            ADD CONSTRAINT fk_agent_onboarding_active_context
            FOREIGN KEY (agent_id, active_context_revision)
            REFERENCES agent_context_revisions(agent_id, revision)
            DEFERRABLE INITIALLY DEFERRED;
    END IF;
END $$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS agent_feed_v2_settings (
    agent_id BIGINT PRIMARY KEY REFERENCES agents(agent_id) ON DELETE CASCADE,
    poll_interval_seconds INT NOT NULL DEFAULT 600,
    explicitly_set BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at BIGINT NOT NULL,
    CONSTRAINT chk_agent_feed_v2_settings_interval
        CHECK (poll_interval_seconds BETWEEN 60 AND 86400)
);

CREATE TABLE IF NOT EXISTS agent_feed_exposures (
    exposure_id BIGSERIAL PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    source_type VARCHAR(32) NOT NULL,
    source_id BIGINT NOT NULL,
    content_class VARCHAR(16) NOT NULL,
    author_agent_id BIGINT NULL REFERENCES agents(agent_id) ON DELETE SET NULL,
    context_revision BIGINT NULL,
    first_seen_at BIGINT NOT NULL,
    last_seen_at BIGINT NOT NULL,
    seen_count BIGINT NOT NULL DEFAULT 1,
    CONSTRAINT uq_agent_feed_exposures_source UNIQUE (agent_id, source_type, source_id),
    CONSTRAINT fk_agent_feed_exposures_context
        FOREIGN KEY (agent_id, context_revision)
        REFERENCES agent_context_revisions(agent_id, revision),
    CONSTRAINT chk_agent_feed_exposures_content_class
        CHECK (content_class IN ('ugc', 'pgc'))
);
CREATE INDEX IF NOT EXISTS idx_agent_feed_exposures_agent_seen
    ON agent_feed_exposures(agent_id, last_seen_at DESC, exposure_id);
CREATE INDEX IF NOT EXISTS idx_agent_feed_exposures_retention
    ON agent_feed_exposures(last_seen_at, exposure_id);

CREATE TABLE IF NOT EXISTS action_executions (
    action_execution_id BIGSERIAL PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    action_type VARCHAR(32) NOT NULL,
    semantic_idempotency_key VARCHAR(128) NOT NULL,
    request_hash VARCHAR(128) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'prepared',
    domain_receipt VARCHAR(256) NULL,
    last_error TEXT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    CONSTRAINT uq_action_executions_semantic
        UNIQUE (agent_id, action_type, semantic_idempotency_key),
    CONSTRAINT chk_action_executions_status
        CHECK (status IN ('prepared', 'dispatched', 'succeeded', 'failed', 'blocked', 'unknown'))
);
CREATE INDEX IF NOT EXISTS idx_action_executions_agent_status
    ON action_executions(agent_id, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS agent_attention_items (
    attention_id BIGSERIAL PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    source_type VARCHAR(32) NOT NULL,
    source_id BIGINT NOT NULL,
    proposed_actions JSONB NOT NULL DEFAULT '[]'::jsonb,
    status VARCHAR(16) NOT NULL DEFAULT 'open',
    created_at BIGINT NOT NULL,
    expires_at BIGINT NULL,
    CONSTRAINT uq_agent_attention_items_agent UNIQUE (agent_id, attention_id),
    CONSTRAINT chk_agent_attention_items_actions CHECK (jsonb_typeof(proposed_actions) = 'array'),
    CONSTRAINT chk_agent_attention_items_status CHECK (status IN ('open', 'acted', 'dismissed', 'expired'))
);
CREATE INDEX IF NOT EXISTS idx_agent_attention_items_agent_status
    ON agent_attention_items(agent_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS agent_attention_intents (
    attention_id BIGINT NOT NULL REFERENCES agent_attention_items(attention_id) ON DELETE CASCADE,
    intent_id BIGINT NOT NULL REFERENCES agent_intent_actions(intent_id) ON DELETE CASCADE,
    PRIMARY KEY (attention_id, intent_id)
);

CREATE TABLE IF NOT EXISTS agent_commands (
    command_id BIGSERIAL PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    attention_id BIGINT NULL,
    command_type VARCHAR(32) NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    payload_hash VARCHAR(128) NOT NULL,
    required_context_revision BIGINT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    idempotency_key VARCHAR(128) NOT NULL,
    claim_owner_runtime_id VARCHAR(128) NULL,
    claim_epoch BIGINT NOT NULL DEFAULT 0,
    claim_token_hash VARCHAR(128) NULL,
    claim_until BIGINT NULL,
    attempt_count INT NOT NULL DEFAULT 0,
    result JSONB NULL,
    created_at BIGINT NOT NULL,
    delivered_at BIGINT NULL,
    completed_at BIGINT NULL,
    CONSTRAINT uq_agent_commands_idempotency UNIQUE (agent_id, idempotency_key),
    CONSTRAINT fk_agent_commands_attention
        FOREIGN KEY (agent_id, attention_id)
        REFERENCES agent_attention_items(agent_id, attention_id),
    CONSTRAINT fk_agent_commands_context
        FOREIGN KEY (agent_id, required_context_revision)
        REFERENCES agent_context_revisions(agent_id, revision),
    CONSTRAINT chk_agent_commands_payload CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT chk_agent_commands_result CHECK (result IS NULL OR jsonb_typeof(result) = 'object'),
    CONSTRAINT chk_agent_commands_status
        CHECK (status IN ('pending', 'notified', 'claimed', 'completed', 'failed', 'expired'))
);
CREATE INDEX IF NOT EXISTS idx_agent_commands_agent_pending
    ON agent_commands(agent_id, status, created_at, command_id);
CREATE INDEX IF NOT EXISTS idx_agent_commands_claim_expiry
    ON agent_commands(status, claim_until)
    WHERE status = 'claimed';
CREATE INDEX IF NOT EXISTS idx_agent_commands_attention_lookup
    ON agent_commands(attention_id)
    WHERE attention_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS agent_activity_heads (
    agent_id BIGINT PRIMARY KEY REFERENCES agents(agent_id) ON DELETE CASCADE,
    current_seq BIGINT NOT NULL DEFAULT 0
);

-- Indexes on existing V1 hot tables are built CONCURRENTLY in migration 000069.

CREATE TABLE IF NOT EXISTS telemetry_events_v2 (
    event_id VARCHAR(128) PRIMARY KEY,
    agent_id BIGINT NULL REFERENCES agents(agent_id) ON DELETE SET NULL,
    install_session_id VARCHAR(128) NULL,
    console_session_id VARCHAR(128) NULL,
    event_type VARCHAR(64) NOT NULL,
    properties JSONB NOT NULL DEFAULT '{}'::jsonb,
    event_at BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    CONSTRAINT chk_telemetry_events_v2_properties CHECK (jsonb_typeof(properties) = 'object')
);
CREATE INDEX IF NOT EXISTS idx_telemetry_events_v2_expiry
    ON telemetry_events_v2(expires_at);
CREATE INDEX IF NOT EXISTS idx_telemetry_events_v2_agent_type
    ON telemetry_events_v2(agent_id, event_type, event_at DESC);

CREATE TABLE IF NOT EXISTS console_usage_sessions (
    session_id VARCHAR(128) NOT NULL,
    time_bucket BIGINT NOT NULL,
    agent_id BIGINT NULL REFERENCES agents(agent_id) ON DELETE SET NULL,
    visible_duration_ms BIGINT NOT NULL DEFAULT 0,
    first_event_at BIGINT NOT NULL,
    last_event_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (session_id, time_bucket),
    CONSTRAINT chk_console_usage_sessions_duration CHECK (visible_duration_ms >= 0)
);
CREATE INDEX IF NOT EXISTS idx_console_usage_sessions_agent_time
    ON console_usage_sessions(agent_id, last_event_at DESC);

COMMIT;

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '5min';

-- Production rollback is feature-flag-only once a V2 identity exists. Older
-- Auth binaries do not understand email_kind/scoped principals and could turn
-- a bound email into an unverified V1 login credential.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_principals LIMIT 1)
       OR EXISTS (SELECT 1 FROM agents WHERE email_kind IN ('internal_alias', 'v2_bound') LIMIT 1) THEN
        RAISE EXCEPTION 'unsafe Console V2 schema rollback: disable V2 feature flags and keep the auth boundary';
    END IF;
END $$;
-- +goose StatementEnd

-- Release hot-table locks before removing the V2-only schema.
ALTER TABLE agent_activity_log
    DROP COLUMN IF EXISTS source_event_id,
    DROP COLUMN IF EXISTS agent_seq;
ALTER TABLE agent_cards
    DROP COLUMN IF EXISTS public_card_generated_at,
    DROP COLUMN IF EXISTS public_card_version;
ALTER TABLE agents
    DROP CONSTRAINT IF EXISTS chk_agents_email_kind,
    DROP COLUMN IF EXISTS email_kind;

BEGIN;

DROP TABLE IF EXISTS console_usage_sessions;
DROP TABLE IF EXISTS telemetry_events_v2;
DROP TABLE IF EXISTS agent_activity_heads;
DROP TABLE IF EXISTS agent_commands;
DROP TABLE IF EXISTS agent_attention_intents;
DROP TABLE IF EXISTS agent_attention_items;
DROP TABLE IF EXISTS action_executions;
DROP TABLE IF EXISTS agent_feed_exposures;
DROP TABLE IF EXISTS agent_feed_v2_settings;
ALTER TABLE agent_onboarding_v2 DROP CONSTRAINT IF EXISTS fk_agent_onboarding_active_context;
ALTER TABLE agent_context_heads DROP CONSTRAINT IF EXISTS fk_agent_context_heads_active_revision;
DROP TABLE IF EXISTS agent_context_revisions;
DROP TABLE IF EXISTS agent_context_heads;
DROP TABLE IF EXISTS agent_intent_actions;
DROP TABLE IF EXISTS agent_network_goals;
DROP TABLE IF EXISTS agent_idempotency_requests;
DROP TABLE IF EXISTS agent_onboarding_drafts;
DROP TABLE IF EXISTS agent_onboarding_v2;
DROP TABLE IF EXISTS v2_email_challenges;
DROP TABLE IF EXISTS console_v2_sessions;
DROP TABLE IF EXISTS console_v2_handoffs;
DROP TABLE IF EXISTS agent_credential_sessions;
DROP TABLE IF EXISTS agent_signature_nonces;
DROP TABLE IF EXISTS agent_bootstrap_grants;
DROP TABLE IF EXISTS agent_email_bindings;
DROP TABLE IF EXISTS agent_principals;

COMMIT;
