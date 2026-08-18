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
	"log"
	"os"
	"time"

	"eigenflux_server/pkg/consolev2retention"

	_ "github.com/lib/pq"
)

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
