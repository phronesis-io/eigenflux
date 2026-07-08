-- +goose Up
-- +goose StatementBegin
-- Stable invite codes for KOL (达人) and channel (渠道) growth attribution.
-- kind='kol' rows are auto-created per agent (one each, enforced by the partial
-- unique index); kind='channel' rows are ops-created via scripts/invite_channel
-- (idempotent by name). A code is resolved into a one-shot install_tokens row at
-- entry time, so the existing install funnel applies unchanged downstream.
CREATE TABLE invite_codes (
  code       TEXT PRIMARY KEY,
  kind       TEXT   NOT NULL,
  agent_id   BIGINT NOT NULL DEFAULT 0,
  name       TEXT   NOT NULL DEFAULT '',
  note       TEXT   NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX uni_invite_codes_kol_agent ON invite_codes(agent_id) WHERE kind = 'kol';
CREATE UNIQUE INDEX uni_invite_codes_channel_name ON invite_codes(name) WHERE kind = 'channel';

-- Which invite code (if any) the install entry carried; partial index keeps the
-- paid-funnel scans unaffected.
ALTER TABLE install_tokens ADD COLUMN invite_code TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_install_tokens_invite_code ON install_tokens(invite_code) WHERE invite_code <> '';

-- Registration attribution (被谁邀请), written first-wins when a login-time
-- install report carries both an invite-coded ref and the agent identity.
ALTER TABLE agents ADD COLUMN invited_by_code  TEXT   NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN inviter_agent_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN invited_at       BIGINT NOT NULL DEFAULT 0;
CREATE INDEX idx_agents_invited_by_code ON agents(invited_by_code) WHERE invited_by_code <> '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_agents_invited_by_code;
ALTER TABLE agents DROP COLUMN invited_at;
ALTER TABLE agents DROP COLUMN inviter_agent_id;
ALTER TABLE agents DROP COLUMN invited_by_code;
DROP INDEX IF EXISTS idx_install_tokens_invite_code;
ALTER TABLE install_tokens DROP COLUMN invite_code;
DROP TABLE invite_codes;
-- +goose StatementEnd
