-- +goose Up
-- +goose StatementBegin
CREATE TABLE content_blacklist_keywords (
  keyword_id   BIGSERIAL PRIMARY KEY,
  keyword      TEXT NOT NULL,
  enabled      BOOLEAN NOT NULL DEFAULT true,
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL
);

CREATE UNIQUE INDEX idx_blacklist_keyword_lower_unique ON content_blacklist_keywords(lower(keyword));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS content_blacklist_keywords;
-- +goose StatementEnd
