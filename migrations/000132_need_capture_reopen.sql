-- +goose Up
ALTER TABLE need_capture_reviews
    ADD COLUMN capture_revision BIGINT NOT NULL DEFAULT 0 CHECK (capture_revision >= 0),
    ADD COLUMN reopened_at BIGINT,
    ADD CONSTRAINT need_capture_reopen_no_need CHECK (reopened_at IS NULL OR outcome = 'no_need');

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM need_capture_reviews WHERE capture_revision > 0) THEN
        RAISE EXCEPTION 'cannot downgrade after Need capture reopens: preserve stale-request fencing';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE need_capture_reviews
    DROP CONSTRAINT need_capture_reopen_no_need,
    DROP COLUMN reopened_at,
    DROP COLUMN capture_revision;
