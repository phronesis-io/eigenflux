-- +goose Up
ALTER TABLE friend_requests ADD COLUMN remark VARCHAR(100) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE friend_requests DROP COLUMN remark;
