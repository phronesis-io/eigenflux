-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- One bounded row per Agent/language. A new local day replaces the previous
-- day in place, so generated Today copy cannot grow without bound.
CREATE TABLE IF NOT EXISTS agent_today_model_briefs (
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    language VARCHAR(16) NOT NULL,
    day VARCHAR(10) NOT NULL,
    facts_hash VARCHAR(64) NOT NULL,
    narrative TEXT NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL,
    generated_at BIGINT NOT NULL DEFAULT 0,
    last_attempt_at BIGINT NOT NULL DEFAULT 0,
    lease_until BIGINT NOT NULL DEFAULT 0,
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (agent_id, language),
    CONSTRAINT chk_agent_today_model_brief_language
        CHECK (language IN ('zh-CN', 'en')),
    CONSTRAINT chk_agent_today_model_brief_day
        CHECK (day ~ '^\d{4}-\d{2}-\d{2}$'),
    CONSTRAINT chk_agent_today_model_brief_hash
        CHECK (facts_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT chk_agent_today_model_brief_status
        CHECK (status IN ('pending', 'ready', 'failed')),
    CONSTRAINT chk_agent_today_model_brief_narrative
        CHECK (char_length(narrative) <= 280)
);

-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP TABLE IF EXISTS agent_today_model_briefs;
