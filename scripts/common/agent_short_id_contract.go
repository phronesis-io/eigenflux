//go:build ignore

// Command agent_short_id_contract closes the expand/backfill rollout after all
// deployed Agent writers assign short IDs. It is intentionally not part of the
// automatic migration chain because the rollback window must be closed first.
package main

import (
	"errors"
	"fmt"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const contractApproval = "AGENT_SHORT_ID_CONTRACT_APPROVED"

type contractCounts struct {
	Missing    int64 `gorm:"column:missing"`
	Invalid    int64 `gorm:"column:invalid"`
	Duplicates int64 `gorm:"column:duplicates"`
}

type indexState struct {
	Exists    bool `gorm:"column:exists"`
	Valid     bool `gorm:"column:valid"`
	Ready     bool `gorm:"column:ready"`
	Live      bool `gorm:"column:live"`
	Unique    bool `gorm:"column:unique_index"`
	HasFilter bool `gorm:"column:has_filter"`
}

func main() {
	if os.Getenv(contractApproval) != "1" {
		fatal(fmt.Errorf("%s=1 is required after confirming all old Agent writers are retired", contractApproval))
	}
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		fatal(errors.New("PG_DSN is required"))
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		fatal(err)
	}
	// Pin the whole contract to one PostgreSQL session. SET lock_timeout and
	// statement_timeout are session-scoped, while CREATE INDEX CONCURRENTLY
	// must stay outside a transaction. Without a pinned connection GORM may
	// execute later DDL on another pool connection with no safety timeout.
	if err := db.Connection(runContract); err != nil {
		fatal(err)
	}
	fmt.Fprintln(os.Stderr, "agent short-id contract complete: missing=0 invalid=0 duplicate_groups=0 not_null=true unique_index=valid")
}

func runContract(db *gorm.DB) error {
	if err := db.Exec(`SET lock_timeout = '5s'`).Error; err != nil {
		return err
	}
	if err := db.Exec(`SET statement_timeout = '30min'`).Error; err != nil {
		return err
	}

	// A NOT VALID check starts rejecting new NULL writes immediately. Validation
	// then proves the backfilled rows without holding one long write-blocking lock.
	if err := db.Exec(`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_agents_short_id_present') THEN
			ALTER TABLE agents ADD CONSTRAINT chk_agents_short_id_present
			CHECK (short_id IS NOT NULL) NOT VALID;
		END IF;
	END $$`).Error; err != nil {
		return err
	}
	if err := db.Exec(`ALTER TABLE agents VALIDATE CONSTRAINT chk_agents_short_id_format`).Error; err != nil {
		return err
	}
	if err := db.Exec(`ALTER TABLE agents VALIDATE CONSTRAINT chk_agents_short_id_present`).Error; err != nil {
		return err
	}

	counts, err := readContractCounts(db)
	if err != nil {
		return err
	}
	if counts.Missing != 0 || counts.Invalid != 0 || counts.Duplicates != 0 {
		return fmt.Errorf("contract preflight failed: missing=%d invalid=%d duplicate_groups=%d", counts.Missing, counts.Invalid, counts.Duplicates)
	}

	state, err := readIndexState(db, "uq_agents_short_id")
	if err != nil {
		return err
	}
	if state.Exists && (!state.Valid || !state.Ready || !state.Live || !state.Unique || state.HasFilter) {
		if err := db.Exec(`DROP INDEX CONCURRENTLY uq_agents_short_id`).Error; err != nil {
			return fmt.Errorf("drop incomplete short-id index: %w", err)
		}
		state = indexState{}
	}
	if !state.Exists {
		if err := db.Exec(`CREATE UNIQUE INDEX CONCURRENTLY uq_agents_short_id
			ON agents (short_id COLLATE "C")`).Error; err != nil {
			return fmt.Errorf("create complete short-id index: %w", err)
		}
	}
	state, err = readIndexState(db, "uq_agents_short_id")
	if err != nil {
		return err
	}
	if !state.Exists || !state.Valid || !state.Ready || !state.Live || !state.Unique || state.HasFilter {
		return fmt.Errorf("complete short-id index is not valid: %+v", state)
	}

	if err := db.Exec(`ALTER TABLE agents ALTER COLUMN short_id SET NOT NULL`).Error; err != nil {
		return err
	}
	if err := db.Exec(`ALTER TABLE agents DROP CONSTRAINT IF EXISTS chk_agents_short_id_present`).Error; err != nil {
		return err
	}
	if err := db.Exec(`DROP INDEX CONCURRENTLY IF EXISTS uq_agents_short_id_partial`).Error; err != nil {
		return err
	}
	return nil
}

func readContractCounts(db *gorm.DB) (contractCounts, error) {
	var counts contractCounts
	err := db.Raw(`SELECT
		COUNT(*) FILTER (WHERE short_id IS NULL) AS missing,
		COUNT(*) FILTER (WHERE short_id IS NOT NULL AND short_id !~ '^[A-Za-z]{5}$') AS invalid,
		(SELECT COUNT(*) FROM (
			SELECT short_id FROM agents WHERE short_id IS NOT NULL
			GROUP BY short_id HAVING COUNT(*) > 1
		) duplicate_ids) AS duplicates
	FROM agents`).Scan(&counts).Error
	return counts, err
}

func readIndexState(db *gorm.DB, indexName string) (indexState, error) {
	var state indexState
	err := db.Raw(`SELECT TRUE AS exists, i.indisvalid AS valid, i.indisready AS ready,
		i.indislive AS live, i.indisunique AS unique_index, i.indpred IS NOT NULL AS has_filter
	FROM pg_index i
	JOIN pg_class c ON c.oid = i.indexrelid
	JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname = current_schema() AND c.relname = ?`, indexName).Scan(&state).Error
	return state, err
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
