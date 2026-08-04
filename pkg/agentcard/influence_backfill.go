package agentcard

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

const maxInfluenceCounterValue int64 = 1_000_000_000_000

// AdvanceInfluenceRollupBackfill performs a bounded, resumable portion of the
// migration-57 backfill. The deployment script normally drains it immediately;
// the cron also calls it so a direct `goose up` cannot leave production in a
// permanently half-migrated state.
func AdvanceInfluenceRollupBackfill(ctx context.Context, gdb *gorm.DB, limit int) (processed int, complete bool, err error) {
	if limit <= 0 {
		limit = 100
	}
	var ready bool
	if err = gdb.WithContext(ctx).Raw(`SELECT backfill_complete FROM agent_influence_rollup_meta WHERE singleton = TRUE`).Scan(&ready).Error; err != nil {
		return 0, false, err
	}
	if ready {
		return 0, true, nil
	}

	var ids []int64
	if err = gdb.WithContext(ctx).Raw(`SELECT agent_id FROM agent_influence_rollup_pending ORDER BY agent_id LIMIT ?`, limit).Scan(&ids).Error; err != nil {
		return 0, false, err
	}
	for _, agentID := range ids {
		if err = backfillInfluenceAgent(ctx, gdb, agentID); err != nil {
			return processed, false, fmt.Errorf("backfill agent %d: %w", agentID, err)
		}
		processed++
	}

	var remaining bool
	if err = gdb.WithContext(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM agent_influence_rollup_pending)`).Scan(&remaining).Error; err != nil {
		return processed, false, err
	}
	if remaining {
		return processed, false, nil
	}
	if err = gdb.WithContext(ctx).Exec(`ALTER TABLE item_stats VALIDATE CONSTRAINT chk_item_stats_agentcard_counters_sane`).Error; err != nil {
		return processed, false, err
	}
	if err = gdb.WithContext(ctx).Exec(`UPDATE agent_influence_rollup_meta SET backfill_complete = TRUE WHERE singleton = TRUE`).Error; err != nil {
		return processed, false, err
	}
	return processed, true, nil
}

func backfillInfluenceAgent(ctx context.Context, gdb *gorm.DB, agentID int64) error {
	return gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended('agent-influence:' || CAST(? AS BIGINT)::text, 0))`, agentID).Error; err != nil {
			return err
		}
		var pending bool
		if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM agent_influence_rollup_pending WHERE agent_id = ?)`, agentID).Scan(&pending).Error; err != nil || !pending {
			return err
		}
		var invalid bool
		if err := tx.Raw(`SELECT EXISTS (
			SELECT 1 FROM item_stats
			WHERE author_agent_id = ? AND (
				consumed_count < 0 OR consumed_count > ?
				OR score_1_count < 0 OR score_1_count > ?
				OR score_2_count < 0 OR score_2_count > ?))`,
			agentID, maxInfluenceCounterValue, maxInfluenceCounterValue, maxInfluenceCounterValue).Scan(&invalid).Error; err != nil {
			return err
		}
		if invalid {
			return fmt.Errorf("item_stats contains out-of-range counters")
		}
		if err := tx.Exec(`DELETE FROM agent_influence_rollups WHERE agent_id = ?`, agentID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO agent_influence_rollups
			(agent_id, shard, score_1_count, score_2_count, broadcast_count, consumed_count, content_revision)
			SELECT author_agent_id,
			       ((item_id % 32 + 32) % 32)::SMALLINT,
			       SUM(score_1_count)::NUMERIC(30,0),
			       SUM(score_2_count)::NUMERIC(30,0),
			       COUNT(*)::NUMERIC(30,0),
			       SUM(consumed_count)::NUMERIC(30,0),
			       COUNT(*)::BIGINT
			FROM item_stats WHERE author_agent_id = ?
			GROUP BY author_agent_id, ((item_id % 32 + 32) % 32)`, agentID).Error; err != nil {
			return err
		}
		return tx.Exec(`DELETE FROM agent_influence_rollup_pending WHERE agent_id = ?`, agentID).Error
	})
}
