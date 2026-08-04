-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30min';

-- Hourly percentile ranking must scale with agents, not historical items.
-- Triggers maintain one compact row per author; content_revision advances for
-- every fact change that can affect score, reach, membership, order or summary.
CREATE TABLE agent_influence_rollups (
    agent_id BIGINT PRIMARY KEY REFERENCES agents(agent_id) ON DELETE CASCADE,
    score BIGINT NOT NULL DEFAULT 0,
    broadcast_count BIGINT NOT NULL DEFAULT 0,
    consumed_count BIGINT NOT NULL DEFAULT 0,
    scored_events BIGINT NOT NULL DEFAULT 0,
    content_revision BIGINT NOT NULL DEFAULT 0
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION maintain_agent_influence_from_stats()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO agent_influence_rollups
            (agent_id, score, broadcast_count, consumed_count, scored_events, content_revision)
        VALUES
            (NEW.author_agent_id,
             NEW.score_1_count + 2 * NEW.score_2_count,
             1, NEW.consumed_count,
             NEW.score_1_count + NEW.score_2_count, 1)
        ON CONFLICT (agent_id) DO UPDATE SET
            score = agent_influence_rollups.score + EXCLUDED.score,
            broadcast_count = agent_influence_rollups.broadcast_count + 1,
            consumed_count = agent_influence_rollups.consumed_count + EXCLUDED.consumed_count,
            scored_events = agent_influence_rollups.scored_events + EXCLUDED.scored_events,
            content_revision = agent_influence_rollups.content_revision + 1;
        RETURN NEW;
    ELSIF TG_OP = 'DELETE' THEN
        UPDATE agent_influence_rollups SET
            score = score - (OLD.score_1_count + 2 * OLD.score_2_count),
            broadcast_count = broadcast_count - 1,
            consumed_count = consumed_count - OLD.consumed_count,
            scored_events = scored_events - (OLD.score_1_count + OLD.score_2_count),
            content_revision = content_revision + 1
        WHERE agent_id = OLD.author_agent_id;
        RETURN OLD;
    END IF;

    IF NEW.author_agent_id = OLD.author_agent_id THEN
        UPDATE agent_influence_rollups SET
            score = score + (NEW.score_1_count + 2 * NEW.score_2_count)
                          - (OLD.score_1_count + 2 * OLD.score_2_count),
            consumed_count = consumed_count + NEW.consumed_count - OLD.consumed_count,
            scored_events = scored_events + (NEW.score_1_count + NEW.score_2_count)
                                           - (OLD.score_1_count + OLD.score_2_count),
            content_revision = content_revision + 1
        WHERE agent_id = NEW.author_agent_id;
    ELSE
        UPDATE agent_influence_rollups SET
            score = score - (OLD.score_1_count + 2 * OLD.score_2_count),
            broadcast_count = broadcast_count - 1,
            consumed_count = consumed_count - OLD.consumed_count,
            scored_events = scored_events - (OLD.score_1_count + OLD.score_2_count),
            content_revision = content_revision + 1
        WHERE agent_id = OLD.author_agent_id;
        INSERT INTO agent_influence_rollups
            (agent_id, score, broadcast_count, consumed_count, scored_events, content_revision)
        VALUES
            (NEW.author_agent_id,
             NEW.score_1_count + 2 * NEW.score_2_count,
             1, NEW.consumed_count,
             NEW.score_1_count + NEW.score_2_count, 1)
        ON CONFLICT (agent_id) DO UPDATE SET
            score = agent_influence_rollups.score + EXCLUDED.score,
            broadcast_count = agent_influence_rollups.broadcast_count + 1,
            consumed_count = agent_influence_rollups.consumed_count + EXCLUDED.consumed_count,
            scored_events = agent_influence_rollups.scored_events + EXCLUDED.scored_events,
            content_revision = agent_influence_rollups.content_revision + 1;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_agent_influence_item_stats
AFTER INSERT OR UPDATE OR DELETE ON item_stats
FOR EACH ROW EXECUTE FUNCTION maintain_agent_influence_from_stats();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bump_agent_content_revision_from_processed()
RETURNS TRIGGER AS $$
DECLARE
    target_item_id BIGINT;
BEGIN
    target_item_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.item_id ELSE NEW.item_id END;
    IF TG_OP = 'INSERT' OR TG_OP = 'DELETE'
       OR NEW.summary IS DISTINCT FROM OLD.summary
       OR NEW.status IS DISTINCT FROM OLD.status THEN
        UPDATE agent_influence_rollups AS rollup
        SET content_revision = content_revision + 1
        FROM item_stats AS stats
        WHERE stats.item_id = target_item_id
          AND rollup.agent_id = stats.author_agent_id;
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_agent_influence_processed_items
AFTER INSERT OR UPDATE OR DELETE ON processed_items
FOR EACH ROW EXECUTE FUNCTION bump_agent_content_revision_from_processed();

INSERT INTO agent_influence_rollups
    (agent_id, score, broadcast_count, consumed_count, scored_events, content_revision)
SELECT a.agent_id,
       COALESCE(SUM(s.score_1_count + 2 * s.score_2_count), 0),
       COUNT(s.item_id),
       COALESCE(SUM(s.consumed_count), 0),
       COALESCE(SUM(s.score_1_count + s.score_2_count), 0),
       COUNT(s.item_id)
FROM agents AS a
LEFT JOIN item_stats AS s ON s.author_agent_id = a.agent_id
GROUP BY a.agent_id;

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP TRIGGER IF EXISTS trg_agent_influence_processed_items ON processed_items;
DROP FUNCTION IF EXISTS bump_agent_content_revision_from_processed();
DROP TRIGGER IF EXISTS trg_agent_influence_item_stats ON item_stats;
DROP FUNCTION IF EXISTS maintain_agent_influence_from_stats();
DROP TABLE IF EXISTS agent_influence_rollups;
