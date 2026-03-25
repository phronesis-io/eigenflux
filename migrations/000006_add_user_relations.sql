-- +goose Up
CREATE TABLE user_relations (
    id          BIGSERIAL PRIMARY KEY,
    from_uid    BIGINT NOT NULL,
    to_uid      BIGINT NOT NULL,
    rel_type    SMALLINT NOT NULL,
    created_at  BIGINT NOT NULL,
    CONSTRAINT uq_relation UNIQUE (from_uid, to_uid, rel_type)
);

CREATE INDEX idx_rel_from ON user_relations(from_uid, rel_type);
CREATE INDEX idx_rel_to ON user_relations(to_uid, rel_type);

CREATE TABLE friend_requests (
    id          BIGINT PRIMARY KEY,
    from_uid    BIGINT NOT NULL,
    to_uid      BIGINT NOT NULL,
    status      SMALLINT NOT NULL DEFAULT 0,
    created_at  BIGINT NOT NULL,
    updated_at  BIGINT NOT NULL,
    CONSTRAINT uq_request UNIQUE (from_uid, to_uid)
);

CREATE INDEX idx_req_pending_to ON friend_requests(to_uid, created_at DESC) WHERE status = 0;
CREATE INDEX idx_req_pending_from ON friend_requests(from_uid, created_at DESC) WHERE status = 0;

-- +goose Down
DROP INDEX IF EXISTS idx_req_pending_from;
DROP INDEX IF EXISTS idx_req_pending_to;
DROP TABLE IF EXISTS friend_requests;

DROP INDEX IF EXISTS idx_rel_to;
DROP INDEX IF EXISTS idx_rel_from;
DROP TABLE IF EXISTS user_relations;
