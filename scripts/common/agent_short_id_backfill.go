//go:build ignore

// Command agent_short_id_backfill assigns cryptographically random public
// short IDs to legacy Agents in small, resumable batches.
package main

import (
	"context"
	"encoding/json"
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
		if err := assignShortIDBatch(db, ids); err != nil {
			fatal(fmt.Errorf("backfill agents %d..%d: %w", ids[0], ids[len(ids)-1], err))
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

type shortIDSeed struct {
	AgentID int64  `json:"agent_id"`
	ShortID string `json:"short_id"`
}

func assignShortIDBatch(db *gorm.DB, agentIDs []int64) error {
	for attempt := 0; attempt < 100; attempt++ {
		seeds := make([]shortIDSeed, 0, len(agentIDs))
		seen := make(map[string]struct{}, len(agentIDs))
		for _, agentID := range agentIDs {
			for {
				shortID, err := agentidentity.GenerateShortID()
				if err != nil {
					return err
				}
				if _, exists := seen[shortID]; exists {
					backfillCollisions++
					continue
				}
				seen[shortID] = struct{}{}
				seeds = append(seeds, shortIDSeed{AgentID: agentID, ShortID: shortID})
				break
			}
		}
		encoded, err := json.Marshal(seeds)
		if err != nil {
			return err
		}
		// One bounded autocommit UPDATE replaces one statement/savepoint per
		// Agent. A rare unique collision aborts only this batch; no earlier batch
		// locks or subtransactions remain open while candidates are regenerated.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = db.WithContext(ctx).Exec(`WITH seeds AS (
			SELECT * FROM jsonb_to_recordset(?::jsonb)
			AS seed(agent_id bigint, short_id text)
		) UPDATE agents agent SET short_id = seed.short_id
		FROM seeds seed
		WHERE agent.agent_id = seed.agent_id AND agent.short_id IS NULL`, string(encoded)).Error
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
