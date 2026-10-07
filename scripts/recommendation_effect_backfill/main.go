// recommendation_effect_backfill re-derives recommendation effect observations
// for explicit Shanghai days, e.g. a day older than the cron's 30-day catch-up
// window. Each day uses the same bounded transaction and advisory lock as the
// cron, so running it alongside pipeline-cron is safe.
package main

import (
	"context"
	"flag"
	"log"
	"strings"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/recommendationmetrics"
)

func main() {
	daysFlag := flag.String("days", "", "comma-separated Shanghai days, e.g. 2026-09-01,2026-09-02")
	flag.Parse()
	var days []string
	for _, day := range strings.Split(*daysFlag, ",") {
		if day = strings.TrimSpace(day); day != "" {
			days = append(days, day)
		}
	}
	if len(days) == 0 {
		log.Fatal("--days is required")
	}
	cfg := config.Load()
	db.Init(cfg.PgDSN)
	conn, err := db.DB.DB()
	if err != nil {
		log.Fatal(err)
	}
	failed := false
	for _, day := range days {
		started := time.Now()
		if err := recommendationmetrics.RefreshDay(context.Background(), conn, day, time.Now()); err != nil {
			log.Printf("day %s failed: %v", day, err)
			failed = true
			continue
		}
		log.Printf("day %s refreshed in %s", day, time.Since(started).Round(time.Millisecond))
	}
	if failed {
		log.Fatal("some days failed")
	}
}
