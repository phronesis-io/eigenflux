-- +goose NO TRANSACTION
-- +goose Up

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_raw_items_author_created_item
    ON raw_items(author_agent_id, created_at, item_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_private_messages_home_activity
    ON private_messages(created_at DESC, msg_id DESC)
    INCLUDE (conv_id, sender_id, receiver_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_relations_home_activity
    ON user_relations(created_at DESC, id DESC)
    INCLUDE (from_uid, to_uid)
    WHERE rel_type = 1;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_cards_home_activity
    ON agent_cards(public_card_generated_at DESC, agent_id)
    WHERE public_card_version > 1;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_commands_home_activity
    ON agent_commands(created_at DESC, command_id DESC)
    INCLUDE (agent_id)
    WHERE command_type = 'task_delegation';

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS idx_agent_commands_home_activity;
DROP INDEX CONCURRENTLY IF EXISTS idx_agent_cards_home_activity;
DROP INDEX CONCURRENTLY IF EXISTS idx_user_relations_home_activity;
DROP INDEX CONCURRENTLY IF EXISTS idx_private_messages_home_activity;
DROP INDEX CONCURRENTLY IF EXISTS idx_raw_items_author_created_item;
