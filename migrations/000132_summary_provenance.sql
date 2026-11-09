-- +goose Up
ALTER TABLE processed_items
 ADD COLUMN summary_origin TEXT NOT NULL DEFAULT 'unknown',
 ADD COLUMN summary_zh_literal TEXT NOT NULL DEFAULT '',
 ADD COLUMN title_zh_literal TEXT NOT NULL DEFAULT '',
 ADD COLUMN summary_zh_key TEXT NOT NULL DEFAULT '',
 ADD COLUMN title_zh_key TEXT NOT NULL DEFAULT '';
ALTER TABLE processed_items ADD CONSTRAINT processed_items_summary_origin_check CHECK (summary_origin IN ('unknown','publisher','platform','none'));

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM processed_items WHERE summary_origin <> 'unknown') THEN
  RAISE EXCEPTION 'summary provenance is in use; retain additive schema for code rollback';
 END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE processed_items DROP COLUMN summary_origin, DROP COLUMN summary_zh_literal, DROP COLUMN title_zh_literal, DROP COLUMN summary_zh_key, DROP COLUMN title_zh_key;
