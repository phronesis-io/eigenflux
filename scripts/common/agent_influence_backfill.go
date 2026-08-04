//go:build ignore

// Command agent_influence_backfill initializes the sharded influence rollup
// without holding DDL locks on item_stats. Each author is fenced independently
// so concurrent fact writes are either included in the absolute snapshot or
// applied by the trigger after it commits.
package main

import (
	"fmt"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const maxCounterValue int64 = 1_000_000_000_000

func main() {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "PG_DSN is required")
		os.Exit(2)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		fatal(err)
	}

	processed := 0
	for {
		var ids []int64
		if err := db.Raw(`SELECT agent_id FROM agent_influence_rollup_pending ORDER BY agent_id LIMIT 100`).Scan(&ids).Error; err != nil {
			fatal(err)
		}
		if len(ids) == 0 {
			break
		}
		for _, agentID := range ids {
			if err := backfillAgent(db, agentID); err != nil {
				fatal(fmt.Errorf("backfill agent %d: %w", agentID, err))
			}
			processed++
			if processed%500 == 0 {
				fmt.Fprintf(os.Stderr, "agent influence backfill: %d agents\n", processed)
			}
		}
	}
	if err := db.Exec(`ALTER TABLE item_stats VALIDATE CONSTRAINT chk_item_stats_agentcard_counters_sane`).Error; err != nil {
		fatal(err)
	}
	if err := db.Exec(`UPDATE agent_influence_rollup_meta SET backfill_complete = TRUE WHERE singleton = TRUE`).Error; err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "agent influence backfill complete: %d agents\n", processed)
}

func backfillAgent(db *gorm.DB, agentID int64) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended('agent-influence:' || CAST(? AS BIGINT)::text, 0))`, agentID).Error; err != nil {
			return err
		}
		var invalid bool
		if err := tx.Raw(`SELECT EXISTS (
			SELECT 1 FROM item_stats
			WHERE author_agent_id = ? AND (
				consumed_count < 0 OR consumed_count > ?
				OR score_1_count < 0 OR score_1_count > ?
				OR score_2_count < 0 OR score_2_count > ?))`,
			agentID, maxCounterValue, maxCounterValue, maxCounterValue).Scan(&invalid).Error; err != nil {
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

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
