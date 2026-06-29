-- +goose Up
-- +goose StatementBegin
-- KOL referral tracking for the AGTI activity (decoupled from EF core).
-- Each KOL gets a unique ref code (01..50). The code rides into the funnel via
-- that KOL's per-ref skill URL (/agti/skills/01), is stored on the session at
-- quiz creation, and is logged at every funnel step so the whole funnel —
-- skills view → quiz start → agent lock → human open → human complete →
-- join initiated — is attributable back to each KOL.
ALTER TABLE agti_sessions
    ADD COLUMN ref VARCHAR(64) NOT NULL DEFAULT '';

-- KOL registry: code -> optional human-readable label. Seeded 01..50; extra
-- codes seen in events still show up via the events table.
CREATE TABLE agti_referrals (
    code       VARCHAR(64) PRIMARY KEY,
    label      VARCHAR(255) NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL DEFAULT 0
);

-- One row per funnel event. View-type events (skills_view, join_view) have no
-- session_id; session-type events do. Counts are aggregated by (ref, event).
CREATE TABLE agti_track_events (
    id         BIGSERIAL PRIMARY KEY,
    ref        VARCHAR(64) NOT NULL DEFAULT '',
    event      VARCHAR(32) NOT NULL,
    session_id VARCHAR(64) NOT NULL DEFAULT '',
    client_ip  VARCHAR(64) NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL
);
CREATE INDEX idx_agti_track_ref_event ON agti_track_events (ref, event);
CREATE INDEX idx_agti_track_created ON agti_track_events (created_at);
CREATE INDEX idx_agti_track_ip_event ON agti_track_events (client_ip, event, created_at);

-- Seed 50 KOL codes 01..50 so the dashboard lists all of them even with zero
-- traffic. Labels can be filled in later.
INSERT INTO agti_referrals (code, created_at)
SELECT lpad(g::text, 2, '0'), 0 FROM generate_series(1, 50) g
ON CONFLICT (code) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE agti_track_events;
DROP TABLE agti_referrals;
ALTER TABLE agti_sessions DROP COLUMN ref;
-- +goose StatementEnd
