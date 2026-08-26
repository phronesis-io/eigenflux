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
	knownConcurrentIndexes := []string{
		"uq_agents_short_id_partial",
		"idx_item_stats_author_score",
		"uq_agent_activity_log_agent_seq",
		"uq_agent_activity_log_source_event",
		"idx_agent_activity_log_agent_log_id",
		"idx_agent_activity_log_created_at",
		"idx_conversations_v2_participant_a",
		"idx_conversations_v2_participant_b",
		"idx_private_messages_v2_receiver_conv_unread",
		"idx_agents_legacy_normalized_email",
	}
	var invalidNames []string
	err = db.Raw(`SELECT c.relname FROM pg_class AS c
		JOIN pg_index AS i ON i.indexrelid = c.oid
		JOIN pg_namespace AS n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname IN ?
		  AND (NOT i.indisvalid OR NOT i.indisready)`, knownConcurrentIndexes).Scan(&invalidNames).Error
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, name := range invalidNames {
		statement := fmt.Sprintf(`DROP INDEX CONCURRENTLY public.%s`, name)
		if err := db.Exec(statement).Error; err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "removed invalid %s before migration retry\n", name)
	}
}
