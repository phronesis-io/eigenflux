-- +goose Up
ALTER TABLE processed_items ADD COLUMN summary_remediation TEXT NOT NULL DEFAULT ''
 CHECK (summary_remediation IN ('','body_only','summary_original'));
CREATE TABLE summary_remediation_audit (
 operation_id TEXT PRIMARY KEY,
 item_id BIGINT NOT NULL REFERENCES raw_items(item_id) ON DELETE CASCADE,
 mode TEXT NOT NULL CHECK (mode IN ('body_only','summary_original')),
 source_hash TEXT NOT NULL,
 before_values JSONB NOT NULL,
 reason TEXT NOT NULL,
 evidence TEXT NOT NULL,
 activated_at BIGINT NOT NULL,
 reversed_at BIGINT,
 projection_verified_at BIGINT
);
CREATE UNIQUE INDEX summary_remediation_active_item ON summary_remediation_audit(item_id) WHERE reversed_at IS NULL;

-- Enforce suppression even for legacy writers which do not know this column.
-- +goose StatementBegin
CREATE FUNCTION enforce_summary_remediation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.summary_remediation <> '' THEN
  NEW.summary_zh := '';
  NEW.summary_zh_literal := '';
  NEW.summary_zh_key := '';
 END IF;
 IF NEW.summary_remediation = 'body_only' THEN
  NEW.summary := '';
  NEW.summary_origin := 'none';
  NEW.title_zh := '';
  NEW.title_zh_literal := '';
  NEW.title_zh_key := '';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER processed_items_summary_remediation BEFORE INSERT OR UPDATE ON processed_items
 FOR EACH ROW EXECUTE FUNCTION enforce_summary_remediation();

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM summary_remediation_audit) OR EXISTS (SELECT 1 FROM processed_items WHERE summary_remediation <> '') THEN
  RAISE EXCEPTION 'retain remediation audit and suppression schema after use';
 END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER processed_items_summary_remediation ON processed_items;
DROP FUNCTION enforce_summary_remediation();
DROP TABLE summary_remediation_audit;
ALTER TABLE processed_items DROP COLUMN summary_remediation;
