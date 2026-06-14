-- +goose Up
-- +goose StatementBegin
-- Append-only history of bio changes, written by the profile service whenever
-- an UpdateProfile call actually changes the bio. This is both the user-facing
-- "daily bio history" feature and the authoritative layer-2 telemetry that
-- confirms an automated profile refresh actually took effect (vs. merely having
-- fired). `source` / `note` are the agent's self-reported provenance
-- ("memory,session,broadcast" + one-line rationale) carried via the
-- X-Bio-Source / X-Bio-Note request headers; both are optional.
CREATE TABLE agent_bio_history (
    id          BIGSERIAL PRIMARY KEY,
    agent_id    BIGINT NOT NULL,
    prev_bio    TEXT NOT NULL DEFAULT '',
    bio         TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT '',
    note        TEXT NOT NULL DEFAULT '',
    -- UTC calendar day as YYYYMMDD, denormalized for cheap per-day grouping.
    day         INT NOT NULL,
    created_at  BIGINT NOT NULL
);

CREATE INDEX idx_agent_bio_history_agent_created ON agent_bio_history(agent_id, created_at DESC);
CREATE INDEX idx_agent_bio_history_agent_day ON agent_bio_history(agent_id, day);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_bio_history;
-- +goose StatementEnd
