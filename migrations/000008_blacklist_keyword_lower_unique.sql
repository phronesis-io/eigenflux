-- +goose Up
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_blacklist_keyword_unique;
CREATE UNIQUE INDEX idx_blacklist_keyword_lower_unique ON content_blacklist_keywords(lower(keyword));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_blacklist_keyword_lower_unique;
CREATE UNIQUE INDEX idx_blacklist_keyword_unique ON content_blacklist_keywords(keyword);
-- +goose StatementEnd
