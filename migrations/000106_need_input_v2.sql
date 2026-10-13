-- +goose Up
-- +goose StatementBegin
ALTER TABLE need_inputs DROP CONSTRAINT need_inputs_schema_version_check;
ALTER TABLE need_inputs ADD CONSTRAINT need_inputs_schema_version_check
    CHECK (schema_version IN ('need_input.v1', 'need_input.v2'));
ALTER TABLE need_inputs DROP CONSTRAINT need_inputs_status_check;
ALTER TABLE need_inputs ADD CONSTRAINT need_inputs_status_check
    CHECK (status IN ('pending', 'normalized', 'failed', 'superseded', 'active'));
ALTER TABLE need_inputs ALTER COLUMN status SET DEFAULT 'active';
-- Historical input JSON, hashes, statuses and projections stay unchanged.
-- Old normalized inputs are directly executable without reading their projection.
CREATE VIEW current_need_inputs AS
SELECT i.* FROM need_inputs i
JOIN agent_intent_actions a ON a.agent_id = i.agent_id AND a.intent_id = i.intent_id
WHERE i.status IN ('active', 'normalized')
    AND a.status = 'active' AND a.version = i.intent_version;
-- +goose StatementEnd

-- +goose Down
-- Refuse a lossy downgrade once new captures exist.
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM need_inputs WHERE schema_version = 'need_input.v2' OR status = 'active') THEN
  RAISE EXCEPTION 'Cannot downgrade NeedInput execution while v2 or active inputs exist';
 END IF;
END $$;
DROP VIEW current_need_inputs;
ALTER TABLE need_inputs ALTER COLUMN status SET DEFAULT 'pending';
ALTER TABLE need_inputs DROP CONSTRAINT need_inputs_status_check;
ALTER TABLE need_inputs ADD CONSTRAINT need_inputs_status_check
    CHECK (status IN ('pending', 'normalized', 'failed', 'superseded'));
ALTER TABLE need_inputs DROP CONSTRAINT need_inputs_schema_version_check;
ALTER TABLE need_inputs ADD CONSTRAINT need_inputs_schema_version_check
    CHECK (schema_version = 'need_input.v1');
-- +goose StatementEnd
