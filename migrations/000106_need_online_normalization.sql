-- +goose Up
ALTER TABLE normalized_needs ADD COLUMN mapping_status TEXT NOT NULL DEFAULT 'unmapped'
    CHECK (mapping_status IN ('unmapped', 'partial', 'mapped'));
CREATE INDEX idx_normalized_needs_mapping ON normalized_needs(mapping_status, normalized_need_id)
    WHERE status = 'active';

-- Recreate the view to expose the added mapping state. Eligibility does not
-- depend on vocabulary coverage or on any offline processing state.
CREATE OR REPLACE VIEW current_normalized_needs AS
SELECT n.* FROM normalized_needs n
JOIN need_inputs i ON i.need_input_id = n.need_input_id
JOIN agent_intent_actions a ON a.agent_id = n.agent_id AND a.intent_id = n.intent_id
WHERE n.status = 'active' AND i.status = 'normalized'
    AND a.status = 'active' AND a.version = n.intent_version;

-- +goose Down
DROP VIEW current_normalized_needs;
DROP INDEX idx_normalized_needs_mapping;
ALTER TABLE normalized_needs DROP COLUMN mapping_status;
CREATE VIEW current_normalized_needs AS
SELECT n.* FROM normalized_needs n
JOIN need_inputs i ON i.need_input_id = n.need_input_id
JOIN agent_intent_actions a ON a.agent_id = n.agent_id AND a.intent_id = n.intent_id
WHERE n.status = 'active' AND i.status = 'normalized'
    AND a.status = 'active' AND a.version = n.intent_version;
