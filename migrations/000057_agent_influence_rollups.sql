-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- Hourly percentile ranking must scale with agents, not historical items.
-- Triggers maintain one compact row per author; content_revision advances for
-- every fact change that can affect score, reach, membership, order or summary.
CREATE TABLE agent_influence_rollups (
    agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    shard SMALLINT NOT NULL,
    score_1_count NUMERIC(30, 0) NOT NULL DEFAULT 0,
    score_2_count NUMERIC(30, 0) NOT NULL DEFAULT 0,
    broadcast_count NUMERIC(30, 0) NOT NULL DEFAULT 0,
    consumed_count NUMERIC(30, 0) NOT NULL DEFAULT 0,
    content_revision BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (agent_id, shard),
    CONSTRAINT chk_agent_influence_rollup_shard CHECK (shard >= 0 AND shard < 32),
    CONSTRAINT chk_agent_influence_rollup_nonnegative CHECK (
        score_1_count >= 0 AND score_2_count >= 0
        AND broadcast_count >= 0 AND consumed_count >= 0)
);

CREATE TABLE agent_influence_rollup_meta (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    backfill_complete BOOLEAN NOT NULL DEFAULT FALSE
);
INSERT INTO agent_influence_rollup_meta(singleton, backfill_complete) VALUES (TRUE, FALSE);

-- Populated only after both fact triggers have been installed. Because the
-- CREATE TRIGGER lock is held until commit, any concurrent item_stats writer
-- is either visible to this final snapshot or runs through the new trigger.
CREATE TABLE agent_influence_rollup_pending (
    agent_id BIGINT PRIMARY KEY REFERENCES agents(agent_id) ON DELETE CASCADE
);

ALTER TABLE item_stats ADD CONSTRAINT chk_item_stats_agentcard_counters_sane
CHECK (consumed_count BETWEEN 0 AND 1000000000000
   AND score_1_count BETWEEN 0 AND 1000000000000
   AND score_2_count BETWEEN 0 AND 1000000000000) NOT VALID;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION maintain_agent_influence_from_stats()
RETURNS TRIGGER AS $$
DECLARE
    old_shard SMALLINT;
    new_shard SMALLINT;
    backfill_done BOOLEAN;
BEGIN
    SELECT backfill_complete INTO backfill_done
    FROM agent_influence_rollup_meta WHERE singleton = TRUE;
    IF NOT backfill_done THEN
        IF TG_OP = 'DELETE' THEN
            PERFORM pg_advisory_xact_lock(hashtextextended('agent-influence:' || OLD.author_agent_id::text, 0));
        ELSIF TG_OP = 'INSERT' THEN
            PERFORM pg_advisory_xact_lock(hashtextextended('agent-influence:' || NEW.author_agent_id::text, 0));
        ELSE
            PERFORM pg_advisory_xact_lock(hashtextextended('agent-influence:' || LEAST(OLD.author_agent_id, NEW.author_agent_id)::text, 0));
            IF OLD.author_agent_id <> NEW.author_agent_id THEN
                PERFORM pg_advisory_xact_lock(hashtextextended('agent-influence:' || GREATEST(OLD.author_agent_id, NEW.author_agent_id)::text, 0));
            END IF;
        END IF;
    END IF;
    old_shard := CASE WHEN TG_OP = 'INSERT' THEN 0 ELSE ((OLD.item_id % 32 + 32) % 32)::SMALLINT END;
    new_shard := CASE WHEN TG_OP = 'DELETE' THEN 0 ELSE ((NEW.item_id % 32 + 32) % 32)::SMALLINT END;

    IF TG_OP = 'INSERT' THEN
        INSERT INTO agent_influence_rollups
            (agent_id, shard, score_1_count, score_2_count, broadcast_count, consumed_count, content_revision)
        VALUES
            (NEW.author_agent_id, new_shard, NEW.score_1_count, NEW.score_2_count, 1, NEW.consumed_count, 1)
        ON CONFLICT (agent_id, shard) DO UPDATE SET
            score_1_count = agent_influence_rollups.score_1_count + EXCLUDED.score_1_count,
            score_2_count = agent_influence_rollups.score_2_count + EXCLUDED.score_2_count,
            broadcast_count = agent_influence_rollups.broadcast_count + 1,
            consumed_count = agent_influence_rollups.consumed_count + EXCLUDED.consumed_count,
            content_revision = agent_influence_rollups.content_revision + 1;
        RETURN NEW;
    ELSIF TG_OP = 'DELETE' THEN
        UPDATE agent_influence_rollups SET
            score_1_count = score_1_count - OLD.score_1_count,
            score_2_count = score_2_count - OLD.score_2_count,
            broadcast_count = broadcast_count - 1,
            consumed_count = consumed_count - OLD.consumed_count,
            content_revision = content_revision + 1
        WHERE agent_id = OLD.author_agent_id AND shard = old_shard;
        RETURN OLD;
    END IF;

    IF NEW.author_agent_id = OLD.author_agent_id AND new_shard = old_shard THEN
        UPDATE agent_influence_rollups SET
            score_1_count = score_1_count + NEW.score_1_count - OLD.score_1_count,
            score_2_count = score_2_count + NEW.score_2_count - OLD.score_2_count,
            consumed_count = consumed_count + NEW.consumed_count - OLD.consumed_count,
            content_revision = content_revision + 1
        WHERE agent_id = NEW.author_agent_id AND shard = new_shard;
    ELSE
        UPDATE agent_influence_rollups SET
            score_1_count = score_1_count - OLD.score_1_count,
            score_2_count = score_2_count - OLD.score_2_count,
            broadcast_count = broadcast_count - 1,
            consumed_count = consumed_count - OLD.consumed_count,
            content_revision = content_revision + 1
        WHERE agent_id = OLD.author_agent_id AND shard = old_shard;
        INSERT INTO agent_influence_rollups
            (agent_id, shard, score_1_count, score_2_count, broadcast_count, consumed_count, content_revision)
        VALUES
            (NEW.author_agent_id, new_shard, NEW.score_1_count, NEW.score_2_count, 1, NEW.consumed_count, 1)
        ON CONFLICT (agent_id, shard) DO UPDATE SET
            score_1_count = agent_influence_rollups.score_1_count + EXCLUDED.score_1_count,
            score_2_count = agent_influence_rollups.score_2_count + EXCLUDED.score_2_count,
            broadcast_count = agent_influence_rollups.broadcast_count + 1,
            consumed_count = agent_influence_rollups.consumed_count + EXCLUDED.consumed_count,
            content_revision = agent_influence_rollups.content_revision + 1;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_agent_influence_item_stats
AFTER INSERT OR DELETE OR UPDATE OF item_id, author_agent_id, consumed_count, score_1_count, score_2_count, total_score ON item_stats
FOR EACH ROW EXECUTE FUNCTION maintain_agent_influence_from_stats();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bump_agent_content_revision_from_processed()
RETURNS TRIGGER AS $$
DECLARE
    old_agent_id BIGINT;
    new_agent_id BIGINT;
    old_shard SMALLINT;
    new_shard SMALLINT;
    backfill_done BOOLEAN;
BEGIN
    IF TG_OP = 'UPDATE'
       AND NEW.item_id IS NOT DISTINCT FROM OLD.item_id
       AND NEW.summary IS NOT DISTINCT FROM OLD.summary
       AND NEW.status IS NOT DISTINCT FROM OLD.status THEN
        RETURN NEW;
    END IF;
    IF TG_OP <> 'INSERT' THEN
        SELECT author_agent_id, ((item_id % 32 + 32) % 32)::SMALLINT
        INTO old_agent_id, old_shard FROM item_stats
        WHERE item_id = OLD.item_id AND total_score > 0
        FOR SHARE;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        SELECT author_agent_id, ((item_id % 32 + 32) % 32)::SMALLINT
        INTO new_agent_id, new_shard FROM item_stats
        WHERE item_id = NEW.item_id AND total_score > 0
        FOR SHARE;
    END IF;
    SELECT backfill_complete INTO backfill_done
    FROM agent_influence_rollup_meta WHERE singleton = TRUE;
    IF NOT backfill_done THEN
        IF old_agent_id IS NOT NULL AND (new_agent_id IS NULL OR old_agent_id <= new_agent_id) THEN
            PERFORM pg_advisory_xact_lock(hashtextextended('agent-influence:' || old_agent_id::text, 0));
        END IF;
        IF new_agent_id IS NOT NULL THEN
            PERFORM pg_advisory_xact_lock(hashtextextended('agent-influence:' || new_agent_id::text, 0));
        END IF;
        IF old_agent_id IS NOT NULL AND new_agent_id IS NOT NULL AND old_agent_id > new_agent_id THEN
            PERFORM pg_advisory_xact_lock(hashtextextended('agent-influence:' || old_agent_id::text, 0));
        END IF;
    END IF;
    IF old_agent_id IS NOT NULL AND (TG_OP = 'DELETE' OR NEW.item_id IS DISTINCT FROM OLD.item_id) THEN
        UPDATE agent_influence_rollups
        SET content_revision = content_revision + 1
        WHERE agent_id = old_agent_id AND shard = old_shard;
    END IF;
    IF new_agent_id IS NOT NULL THEN
        UPDATE agent_influence_rollups
        SET content_revision = content_revision + 1
        WHERE agent_id = new_agent_id AND shard = new_shard;
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_agent_influence_processed_items
AFTER INSERT OR UPDATE OR DELETE ON processed_items
FOR EACH ROW EXECUTE FUNCTION bump_agent_content_revision_from_processed();

INSERT INTO agent_influence_rollup_pending(agent_id)
SELECT agent_id FROM agents
ON CONFLICT DO NOTHING;

-- Historical rows are backfilled online by scripts/common/agent_influence_backfill.go.
-- Keeping the scan outside this DDL transaction avoids holding trigger locks
-- on the hot fact tables for the duration of a full-history aggregation.

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP TRIGGER IF EXISTS trg_agent_influence_processed_items ON processed_items;
DROP FUNCTION IF EXISTS bump_agent_content_revision_from_processed();
DROP TRIGGER IF EXISTS trg_agent_influence_item_stats ON item_stats;
DROP FUNCTION IF EXISTS maintain_agent_influence_from_stats();
ALTER TABLE item_stats DROP CONSTRAINT IF EXISTS chk_item_stats_agentcard_counters_sane;
DROP TABLE IF EXISTS agent_influence_rollup_pending;
DROP TABLE IF EXISTS agent_influence_rollup_meta;
DROP TABLE IF EXISTS agent_influence_rollups;
