-- +goose Up
-- A Redis lease is only advisory. The per-rebuild sequence is a durable
-- fencing token: a holder that resumes after its lease expired cannot overwrite
-- a projection accepted by a newer holder.
ALTER TABLE agent_cards
    ADD COLUMN rebuild_fence BIGINT NOT NULL DEFAULT 0;

CREATE SEQUENCE agent_card_rebuild_fence_seq AS BIGINT START WITH 1;

-- +goose Down
DROP SEQUENCE IF EXISTS agent_card_rebuild_fence_seq;

ALTER TABLE agent_cards
    DROP COLUMN IF EXISTS rebuild_fence;
