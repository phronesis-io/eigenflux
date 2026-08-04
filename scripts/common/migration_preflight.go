//go:build ignore

// Command migration_preflight repairs only known invalid concurrent indexes.
// It deliberately leaves valid indexes untouched so a Goose retry after a
// successful CREATE is non-destructive.
package main

import (
	"fmt"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "PG_DSN is required")
		os.Exit(2)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var invalid bool
	err = db.Raw(`SELECT EXISTS (
		SELECT 1 FROM pg_class AS c
		JOIN pg_index AS i ON i.indexrelid = c.oid
		WHERE c.relname = 'idx_item_stats_author_score'
		  AND NOT i.indisvalid
	)`).Scan(&invalid).Error
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !invalid {
		return
	}
	if err := db.Exec(`DROP INDEX CONCURRENTLY idx_item_stats_author_score`).Error; err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "removed invalid idx_item_stats_author_score before migration retry")
}
