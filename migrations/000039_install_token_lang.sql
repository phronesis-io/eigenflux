-- +goose Up
-- +goose StatementBegin
-- Entry language the visitor saw on the /install landing page ('en'/'zh'),
-- captured at mint. Lets paid conversion be broken down by language — i.e.
-- whether the English or Chinese version of the page converts better for a
-- given channel (小红书).
ALTER TABLE install_tokens ADD COLUMN lang VARCHAR(8) NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE install_tokens DROP COLUMN lang;
-- +goose StatementEnd
