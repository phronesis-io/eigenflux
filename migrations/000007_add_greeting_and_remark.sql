-- +goose Up
ALTER TABLE friend_requests ADD COLUMN greeting TEXT NOT NULL DEFAULT '';
ALTER TABLE user_relations ADD COLUMN remark TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE user_relations DROP COLUMN IF EXISTS remark;
ALTER TABLE friend_requests DROP COLUMN IF EXISTS greeting;
