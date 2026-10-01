-- +goose Up
CREATE TABLE item_publish_outbox (
    item_id BIGINT PRIMARY KEY REFERENCES raw_items(item_id) ON DELETE CASCADE,
    created_at BIGINT NOT NULL,
    dispatched_at BIGINT
);
CREATE INDEX idx_item_publish_outbox_pending ON item_publish_outbox(item_id) WHERE dispatched_at IS NULL;
CREATE INDEX idx_item_publish_outbox_acked ON item_publish_outbox(item_id) WHERE dispatched_at IS NOT NULL;

-- +goose Down
LOCK TABLE item_publish_outbox IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM item_publish_outbox WHERE dispatched_at IS NULL) THEN
        RAISE EXCEPTION 'Cannot remove undispatched item publications';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE item_publish_outbox;
