// Command backfill_surface_history rebuilds the online Swing seed projection
// from durable followup_labels rows. It is idempotent and safe to run while the
// FollowupConsumer writes live events because the shared store uses ZADD GT.
//
//	go run ./scripts/recall/backfill_surface_history --dry-run
//	go run ./scripts/recall/backfill_surface_history
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/recall"
)

const surfaceBackfillQuery = `
WITH latest_surface AS MATERIALIZED (
  SELECT agent_id, item_id, MAX(reported_at) AS last_surface_at
  FROM followup_labels
  WHERE kind = 'surface'
    AND reported_at >= ?
    AND reported_at <= ?
  GROUP BY agent_id, item_id
), ranked AS (
  SELECT agent_id, item_id, last_surface_at,
         ROW_NUMBER() OVER (
           PARTITION BY agent_id
           ORDER BY last_surface_at DESC, item_id DESC
         ) AS recency_rank
  FROM latest_surface
)
SELECT agent_id, item_id, last_surface_at
FROM ranked
WHERE recency_rank <= ?
ORDER BY agent_id, last_surface_at DESC, item_id DESC`

func main() {
	dryRun := flag.Bool("dry-run", false, "count matching surface history without writing Redis")
	batchSize := flag.Int("batch-size", 1000, "number of rows per Redis transaction")
	flag.Parse()
	if *batchSize <= 0 {
		log.Fatal("batch-size must be > 0")
	}

	cfg := config.Load()
	db.Init(cfg.PgDSN)
	ctx := context.Background()
	now := time.Now()
	rows, err := db.DB.WithContext(ctx).Raw(
		surfaceBackfillQuery,
		now.Add(-recall.SurfaceHistoryTTL).UnixMilli(),
		now.UnixMilli(),
		recall.SurfaceHistoryMaxItems,
	).Rows()
	if err != nil {
		log.Fatalf("query surface history: %v", err)
	}
	defer rows.Close()

	var store *recall.SurfaceHistoryStore
	if !*dryRun {
		mq.Init(cfg.RedisAddr, cfg.RedisPassword)
		store = recall.NewSurfaceHistoryStore(mq.RDB, cfg.RecallRedisNamespace)
	}

	batch := make([]recall.SurfaceEvent, 0, *batchSize)
	agents := make(map[int64]struct{})
	rowCount := 0
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if !*dryRun {
			if err := store.Upsert(ctx, batch); err != nil {
				log.Fatalf("write surface history after %d rows: %v", rowCount, err)
			}
		}
		batch = batch[:0]
	}

	for rows.Next() {
		var event recall.SurfaceEvent
		if err := rows.Scan(&event.AgentID, &event.ItemID, &event.ReportedAt); err != nil {
			log.Fatalf("scan surface history: %v", err)
		}
		agents[event.AgentID] = struct{}{}
		rowCount++
		batch = append(batch, event)
		if len(batch) >= *batchSize {
			flush()
			if !*dryRun {
				log.Printf("surface history progress rows=%d agents=%d", rowCount, len(agents))
			}
		}
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("iterate surface history: %v", err)
	}
	flush()

	mode := "written"
	if *dryRun {
		mode = "matched (dry-run)"
	}
	log.Printf("surface history backfill %s rows=%d agents=%d cutoff=%s", mode, rowCount, len(agents), now.Add(-recall.SurfaceHistoryTTL).UTC().Format(time.RFC3339))
}
