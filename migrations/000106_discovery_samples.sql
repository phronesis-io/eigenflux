-- +goose Up
ALTER TABLE replay_logs ALTER COLUMN item_id DROP NOT NULL;
ALTER TABLE replay_logs
 ADD COLUMN pipeline_version TEXT NOT NULL DEFAULT 'legacy_feed_v1',
 ADD COLUMN request_mode TEXT NOT NULL DEFAULT 'feed',
 ADD COLUMN sample_schema_version INTEGER NOT NULL DEFAULT 1,
 ADD COLUMN source_kind TEXT NOT NULL DEFAULT 'broadcast',
 ADD COLUMN source_id BIGINT,
 ADD COLUMN context_id BIGINT,
 ADD COLUMN need_id BIGINT,
 ADD COLUMN need_revision BIGINT;
ALTER TABLE replay_logs ADD CONSTRAINT replay_source_identity CHECK (
 (source_kind='broadcast' AND item_id IS NOT NULL AND (source_id IS NULL OR source_id=item_id)) OR
 (source_kind IN ('commission','agent') AND item_id IS NULL AND source_id IS NOT NULL AND source_id>0));
CREATE INDEX idx_replay_discovery_mode_time ON replay_logs(pipeline_version,request_mode,served_at);
CREATE INDEX idx_replay_discovery_context ON replay_logs(context_id,served_at) WHERE context_id IS NOT NULL;
-- +goose Down
-- Typed samples must be archived before a downgrade; never coerce their IDs into item_id.
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM replay_logs WHERE item_id IS NULL) THEN
  RAISE EXCEPTION 'Archive typed discovery samples before downgrading';
 END IF;
END $$;
-- +goose StatementEnd
DROP INDEX idx_replay_discovery_context;
DROP INDEX idx_replay_discovery_mode_time;
ALTER TABLE replay_logs DROP CONSTRAINT replay_source_identity;
ALTER TABLE replay_logs DROP COLUMN pipeline_version,DROP COLUMN request_mode,DROP COLUMN sample_schema_version,DROP COLUMN source_kind,DROP COLUMN source_id,DROP COLUMN context_id,DROP COLUMN need_id,DROP COLUMN need_revision;
ALTER TABLE replay_logs ALTER COLUMN item_id SET NOT NULL;
