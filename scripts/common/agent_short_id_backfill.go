//go:build ignore

// Command agent_short_id_backfill assigns cryptographically random public
// short IDs to legacy Agents in small, resumable batches.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"eigenflux_server/pkg/agentidentity"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const shortIDBackfillBatch = 100

var backfillCollisions int64

func main() {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		fatal(errors.New("PG_DSN is required"))
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		fatal(err)
	}

	var cursor int64
	var processed int64
	for {
		var ids []int64
		if err := db.Raw(`SELECT agent_id FROM agents
			WHERE short_id IS NULL AND agent_id > ?
			ORDER BY agent_id LIMIT ?`, cursor, shortIDBackfillBatch).Scan(&ids).Error; err != nil {
			fatal(err)
		}
		if len(ids) == 0 {
			break
		}
		for _, agentID := range ids {
			if err := assignShortID(db, agentID); err != nil {
				fatal(fmt.Errorf("backfill agent %d: %w", agentID, err))
			}
		}
		cursor = ids[len(ids)-1]
		processed += int64(len(ids))
		fmt.Fprintf(os.Stderr, "agent short-id backfill: processed=%d collisions=%d\n", processed, backfillCollisions)
	}
	if err := db.Exec(`ALTER TABLE agents VALIDATE CONSTRAINT chk_agents_short_id_format`).Error; err != nil {
		fatal(err)
	}
	var remaining int64
	if err := db.Raw(`SELECT COUNT(*) FROM agents WHERE short_id IS NULL`).Scan(&remaining).Error; err != nil {
		fatal(err)
	}
	if remaining != 0 {
		fatal(fmt.Errorf("short-id backfill incomplete: %d agents remain", remaining))
	}
	fmt.Fprintf(os.Stderr, "agent short-id backfill complete: processed=%d remaining=%d collisions=%d failures=0\n", processed, remaining, backfillCollisions)
}

func assignShortID(db *gorm.DB, agentID int64) error {
	for attempt := 0; attempt < 100; attempt++ {
		shortID, err := agentidentity.GenerateShortID()
		if err != nil {
			return err
		}
		// Each candidate is one autocommit statement. A unique collision aborts
		// only that statement, avoiding hundreds of nested SAVEPOINTs and row
		// locks held across an entire deployment batch.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = db.WithContext(ctx).Exec(`UPDATE agents SET short_id = ?
			WHERE agent_id = ? AND short_id IS NULL`, shortID, agentID).Error
		cancel()
		if err == nil {
			return nil
		}
		if sqlState(err) != "23505" {
			return err
		}
		backfillCollisions++
	}
	return errors.New("short-id collision retry budget exhausted")
}

func sqlState(err error) string {
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		return state.SQLState()
	}
	return ""
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
