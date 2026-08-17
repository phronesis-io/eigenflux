// Command console_v2_cleanup applies the Console V2 retention matrix in small,
// indexed batches. It is a dry-run plan by default; writes require --apply.
//
//	PG_DSN=... go run ./scripts/console_v2_cleanup
//	PG_DSN=... go run ./scripts/console_v2_cleanup --apply --batch-size=1000 --time-budget=30s
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"eigenflux_server/pkg/consolev2retention"

	_ "github.com/lib/pq"
)

const dayMS = int64(24 * time.Hour / time.Millisecond)

type cleanupJob struct {
	name string
	sql  string
}

func jobs() []cleanupJob {
	shared := consolev2retention.Jobs()
	result := make([]cleanupJob, 0, len(shared))
	for _, job := range shared {
		result = append(result, cleanupJob{name: job.Name, sql: job.SQL})
	}
	return result
}

// legacyJobs retains the original command-local matrix for source-level
// compatibility. Runtime execution uses the shared matrix above so the cron
// and operator command cannot drift.
func legacyJobs() []cleanupJob {
	return []cleanupJob{
		{"bootstrap_grants", boundedDelete("agent_bootstrap_grants", "expires_at < clock_ms() - 7*day_ms()")},
		{"signature_nonces", boundedDelete("agent_signature_nonces", "expires_at < clock_ms() - 7*day_ms()")},
		{"email_challenges", boundedDelete("v2_email_challenges", "expires_at < clock_ms() - 30*day_ms()")},
		{"handoffs", boundedDelete("console_v2_handoffs", "expires_at < clock_ms() - 7*day_ms()")},
		{"console_sessions", boundedDelete("console_v2_sessions", "absolute_expires_at < clock_ms() - 90*day_ms()")},
		{"credential_sessions", boundedDelete("agent_credential_sessions", "absolute_expires_at < clock_ms() - 90*day_ms()")},
		{"idempotency_responses", boundedDelete("agent_idempotency_requests", "expires_at < clock_ms()")},
		{"telemetry_events", boundedDelete("telemetry_events_v2", "expires_at < clock_ms()")},
		{"runtime_leases", boundedDelete("agent_runtime_leases", "lease_until < clock_ms() - day_ms()")},
		{"control_outbox", boundedDelete("control_wakeup_outbox", "status IN ('delivered','dead') AND created_at < clock_ms() - 7*day_ms()")},
		{"feed_payload_redaction", `WITH constants AS (
			SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS clock_ms, 86400000::bigint AS day_ms
		), target AS (
			SELECT item.ctid FROM feed_batch_items item JOIN feed_batches batch ON batch.batch_id = item.batch_id
			CROSS JOIN constants
			WHERE batch.status IN ('acked','dead','expired') AND batch.created_at < constants.clock_ms - 7*constants.day_ms
			  AND COALESCE(item.payload_snapshot->>'redacted', 'false') <> 'true'
			ORDER BY batch.created_at, item.batch_item_id LIMIT $1
		)
		UPDATE feed_batch_items item SET payload_snapshot = jsonb_build_object(
			'source_ref', item.payload_snapshot->'source_ref', 'redacted', true),
			intent_match_snapshot = CASE WHEN item.intent_match_snapshot IS NULL THEN NULL ELSE
				jsonb_build_object('status', item.intent_match_snapshot->'status',
				'matched_intent_ids', COALESCE(item.intent_match_snapshot->'matched_intent_ids', '[]'::jsonb)) END,
			last_error = NULL, updated_at = constants.clock_ms
		FROM target CROSS JOIN constants WHERE item.ctid = target.ctid`},
		{"terminal_consumer_state", `WITH constants AS (
			SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS clock_ms, 86400000::bigint AS day_ms
		), target AS (
			SELECT state.ctid FROM feed_consumer_state state JOIN feed_batches batch ON batch.batch_id = state.active_batch_id
			CROSS JOIN constants
			WHERE batch.status IN ('acked','dead','expired') AND batch.created_at < constants.clock_ms - 30*constants.day_ms
			ORDER BY batch.created_at, batch.batch_id LIMIT $1
		)
		UPDATE feed_consumer_state state SET active_batch_id = NULL, updated_at = constants.clock_ms
		FROM target CROSS JOIN constants WHERE state.ctid = target.ctid`},
		{"feed_batches", boundedDelete("feed_batches", "status IN ('acked','dead','expired') AND created_at < clock_ms() - 30*day_ms() AND NOT EXISTS (SELECT 1 FROM feed_consumer_state state WHERE state.active_batch_id = row.batch_id)")},
		{"commands", boundedDelete("agent_commands", "status IN ('completed','failed','expired') AND COALESCE(completed_at, created_at) < clock_ms() - 30*day_ms()")},
		{"attention_items", boundedDelete("agent_attention_items", "status IN ('acted','dismissed','expired') AND created_at < clock_ms() - 90*day_ms() AND NOT EXISTS (SELECT 1 FROM agent_commands command WHERE command.attention_id = row.attention_id)")},
		{"activity", boundedDelete("agent_activity_log", "created_at < clock_ms() - 90*day_ms()")},
	}
}

// Each statement defines clock_ms/day_ms locally so it stays a single bounded
// database round trip and never requires a permanent database function.
func boundedDelete(table, predicate string) string {
	return fmt.Sprintf(`WITH constants AS (
		SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS clock_ms,
		       %d::bigint AS day_ms
	), target AS (
		SELECT row.ctid FROM %s row CROSS JOIN constants
		WHERE %s LIMIT $1
	)
	DELETE FROM %s row USING target WHERE row.ctid = target.ctid`, dayMS, table,
		qualifyConstants(predicate), table)
}

func qualifyConstants(predicate string) string {
	predicate = replaceAll(predicate, "clock_ms()", "constants.clock_ms")
	return replaceAll(predicate, "day_ms()", "constants.day_ms")
}

func replaceAll(value, old, replacement string) string {
	for {
		index := -1
		for i := 0; i+len(old) <= len(value); i++ {
			if value[i:i+len(old)] == old {
				index = i
				break
			}
		}
		if index < 0 {
			return value
		}
		value = value[:index] + replacement + value[index+len(old):]
	}
}

func main() {
	apply := flag.Bool("apply", false, "apply retention mutations; default is a read-only plan")
	batchSize := flag.Int("batch-size", 1000, "rows per statement (1-5000)")
	timeBudget := flag.Duration("time-budget", 30*time.Second, "maximum wall time for one run (5s-10m)")
	flag.Parse()
	if *batchSize < 1 || *batchSize > 5000 {
		log.Fatal("--batch-size must be between 1 and 5000")
	}
	if *timeBudget < 5*time.Second || *timeBudget > 10*time.Minute {
		log.Fatal("--time-budget must be between 5s and 10m")
	}
	cleanupJobs := jobs()
	if !*apply {
		for _, job := range cleanupJobs {
			log.Printf("dry-run plan: %s (bounded to %d rows per statement)", job.name, *batchSize)
		}
		log.Println("no data changed; pass --apply to execute")
		return
	}
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		log.Fatal("PG_DSN is required with --apply")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)
	deadline := time.Now().Add(*timeBudget)
	totals := make(map[string]int64, len(cleanupJobs))
	completed := make(map[string]bool, len(cleanupJobs))
	for time.Now().Before(deadline) {
		progress := false
		for _, job := range cleanupJobs {
			if completed[job.name] || time.Now().After(deadline) {
				continue
			}
			statementContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			result, execErr := db.ExecContext(statementContext, job.sql, *batchSize)
			cancel()
			if execErr != nil {
				log.Fatalf("cleanup %s: %v", job.name, execErr)
			}
			count, _ := result.RowsAffected()
			totals[job.name] += count
			if count < int64(*batchSize) {
				completed[job.name] = true
			}
			progress = progress || count > 0
		}
		if !progress {
			break
		}
	}
	for _, job := range cleanupJobs {
		log.Printf("cleanup result: %s=%d", job.name, totals[job.name])
	}
	if time.Now().After(deadline) {
		log.Println("time budget reached; rerun safely to continue")
	}
}
