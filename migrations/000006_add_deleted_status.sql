-- +goose Up
-- Add deleted status (5) for user-initiated deletion
-- This is distinct from discarded (4) which is for pipeline quality rejection
ALTER TABLE processed_items
  DROP CONSTRAINT chk_processed_items_status,
  ADD CONSTRAINT chk_processed_items_status CHECK (status IN (0,1,2,3,4,5));

COMMENT ON COLUMN processed_items.status IS 'Processing status: 0=pending, 1=processing, 2=failed, 3=completed, 4=discarded, 5=deleted';

-- +goose Down
ALTER TABLE processed_items
  DROP CONSTRAINT chk_processed_items_status,
  ADD CONSTRAINT chk_processed_items_status CHECK (status IN (0,1,2,3,4));

COMMENT ON COLUMN processed_items.status IS 'Processing status: 0=pending, 1=processing, 2=failed, 3=completed, 4=discarded';
