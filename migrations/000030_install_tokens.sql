-- +goose Up
-- +goose StatementBegin
-- Marketing landing-page (/install) attribution. One row = one minted invite
-- token. status flips pending->installed exactly once on the first /report
-- (the conversion); report_count counts every raw report hit (incl. replays).
-- All UTM/referrer data lives here and is recovered by token on report — the
-- token is an opaque key with no embedded attribution. Public, no auth; minting
-- is IP rate limited at the gateway.
CREATE TABLE install_tokens (
    token        VARCHAR(32)  PRIMARY KEY,
    utm_source   VARCHAR(255) NOT NULL DEFAULT '',
    utm_medium   VARCHAR(255) NOT NULL DEFAULT '',
    utm_campaign VARCHAR(255) NOT NULL DEFAULT '',
    utm_content  VARCHAR(255) NOT NULL DEFAULT '',
    utm_term     VARCHAR(255) NOT NULL DEFAULT '',
    channel      VARCHAR(32)  NOT NULL DEFAULT '',
    referrer     TEXT         NOT NULL DEFAULT '',
    status       VARCHAR(20)  NOT NULL DEFAULT 'pending',
    report_count INT          NOT NULL DEFAULT 0,
    client_ip    VARCHAR(64)  NOT NULL DEFAULT '',
    created_at   BIGINT       NOT NULL,
    reported_at  BIGINT       NOT NULL DEFAULT 0
);

-- Time-series funnel (mints over time) and per-channel rollups.
CREATE INDEX idx_install_tokens_created ON install_tokens (created_at);
CREATE INDEX idx_install_tokens_channel ON install_tokens (channel);
-- Conversion funnel only ever scans installed rows; a plain status index is
-- near-useless (two values), so make it partial.
CREATE INDEX idx_install_tokens_installed ON install_tokens (created_at) WHERE status = 'installed';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS install_tokens;
-- +goose StatementEnd
