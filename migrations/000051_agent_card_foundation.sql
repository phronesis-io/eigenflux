-- +goose Up
-- +goose StatementBegin
-- Agent Card foundation (方案-Agent-Card-脱水, Phase 1+2).
--
-- profile_version: optimistic-lock counter for field-level profile writes
-- (PUT /agents/me/profile/fields carries expected_version; a conditional
-- UPDATE ... WHERE profile_version = expected rejects stale writers).
-- profile_data: extended editable profile fields (seeking/offering/geo/...)
-- as one JSONB document. Field-level ownership/validation lives in code
-- (pkg/agentcard registry); nothing here is queried by SQL, so no GIN index —
-- fields that ever need SQL filtering must be flattened into real columns.
ALTER TABLE agent_profiles ADD COLUMN profile_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_profiles ADD COLUMN profile_data JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Generic per-field change audit. agent_bio_history keeps its narrow legacy
-- semantics (bio only); this table records every field-level profile change
-- so agents can see prev value / actor / time / reason before refreshing.
-- Bio changes made through the new fields endpoint are recorded here AND in
-- agent_bio_history (legacy readers keep working).
CREATE TABLE agent_profile_change_events (
    id               BIGSERIAL PRIMARY KEY,
    agent_id         BIGINT NOT NULL,
    source_version   BIGINT NOT NULL,
    actor_type       VARCHAR(20) NOT NULL,
    actor_id         VARCHAR(100) NOT NULL DEFAULT '',
    source           VARCHAR(100) NOT NULL DEFAULT '',
    reason           TEXT NOT NULL DEFAULT '',
    changed_paths    JSONB NOT NULL DEFAULT '[]'::jsonb,
    previous_values  JSONB NOT NULL DEFAULT '{}'::jsonb,
    new_values       JSONB NOT NULL DEFAULT '{}'::jsonb,
    request_id       VARCHAR(100) NOT NULL DEFAULT '',
    created_at       BIGINT NOT NULL
);
CREATE INDEX idx_agent_profile_events_agent_created
    ON agent_profile_change_events(agent_id, created_at DESC);

-- Rebuildable read projection; never a source of truth. public_card is the
-- whitelist DTO served to other agents, private_card only to the owner.
-- source_version guards against an older rebuild overwriting a newer one
-- (upsert refuses when incoming source_version < stored). card_version is a
-- monotonic per-agent revision bumped on every accepted rebuild.
CREATE TABLE agent_cards (
    agent_id         BIGINT PRIMARY KEY REFERENCES agents(agent_id) ON DELETE CASCADE,
    public_card      JSONB NOT NULL DEFAULT '{}'::jsonb,
    private_card     JSONB NOT NULL DEFAULT '{}'::jsonb,
    schema_version   INT NOT NULL DEFAULT 1,
    source_version   BIGINT NOT NULL DEFAULT 0,
    card_version     BIGINT NOT NULL DEFAULT 1,
    generated_at     BIGINT NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE agent_cards;
DROP INDEX IF EXISTS idx_agent_profile_events_agent_created;
DROP TABLE agent_profile_change_events;
ALTER TABLE agent_profiles DROP COLUMN profile_data;
ALTER TABLE agent_profiles DROP COLUMN profile_version;
-- +goose StatementEnd
