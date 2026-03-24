-- +goose Up
-- Add discarded status (4) for items rejected by LLM quality/content checks
ALTER TABLE processed_items
  DROP CONSTRAINT chk_processed_items_status,
  ADD CONSTRAINT chk_processed_items_status CHECK (status IN (0,1,2,3,4));

-- +goose Down
ALTER TABLE processed_items
  DROP CONSTRAINT chk_processed_items_status,
  ADD CONSTRAINT chk_processed_items_status CHECK (status IN (0,1,2,3));
